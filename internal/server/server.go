// Package server is the hopdesk signaling server. It lets agents register by
// ID, passes the WebRTC offer/answer between a viewer and an agent, and serves
// the browser viewer. Screen data never goes through it, and it never sees
// passwords: authentication happens end to end between viewer and agent.
//
// It's used by cmd/server and by the desktop app's "this computer is the
// server" option.
package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"hopdesk/internal/proto"
)

//go:embed web
var webFS embed.FS

// Config holds the server settings.
type Config struct {
	Addr        string // listen address, e.g. ":8080"
	AgentKey    string // secret agents must present (16+ characters)
	STUN        string // STUN URL, "" to disable
	TURN        string // TURN URL (optional)
	TURNSecret  string // coturn static-auth-secret for short-lived credentials
	TURNUser    string // static TURN credentials (prefer TURNSecret)
	TURNPass    string
	TLSCert     string // serve HTTPS when set
	TLSKey      string
	BehindProxy bool   // trust X-Forwarded-* from a proxy on this machine
	AllowOrigin string // extra viewer origins, comma-separated
}

// DefaultSTUN is Google's public STUN server.
const DefaultSTUN = "stun:stun.l.google.com:19302"

// RandomKey returns a new random agent key.
func RandomKey() string { return randHex(24) }

// ---- per-IP limits ----

const (
	maxConnsPerIP   = 16              // open WebSockets from one address
	offerBurst      = 5               // connection attempts allowed at once…
	offerEvery      = 6 * time.Second // …then one per 6 s (10/min) per address
	agentOfferBurst = 10              // attempts per device, from all addresses…
	agentOfferEvery = 3 * time.Second // …then 20/min
	badKeyLimit     = 5               // wrong agent keys before a lockout
	badKeyLockout   = 15 * time.Minute
)

type bucket struct {
	tokens float64
	last   time.Time
}

func (b *bucket) take(burst int, every time.Duration) bool {
	now := time.Now()
	if b.last.IsZero() {
		b.tokens = float64(burst)
	} else {
		b.tokens = min(float64(burst), b.tokens+now.Sub(b.last).Seconds()/every.Seconds())
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type ipState struct {
	conns    int
	offers   bucket
	badKeys  int
	badUntil time.Time
	seen     time.Time
}

type limiter struct {
	mu     sync.Mutex
	ips    map[string]*ipState
	agents map[string]*bucket
}

func newLimiter(ctx context.Context) *limiter {
	l := &limiter{ips: map[string]*ipState{}, agents: map[string]*bucket{}}
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			l.mu.Lock()
			for ip, s := range l.ips {
				if s.conns == 0 && time.Since(s.seen) > 30*time.Minute && time.Now().After(s.badUntil) {
					delete(l.ips, ip)
				}
			}
			l.mu.Unlock()
		}
	}()
	return l
}

func (l *limiter) get(ip string) *ipState {
	s := l.ips[ip]
	if s == nil {
		s = &ipState{}
		l.ips[ip] = s
	}
	s.seen = time.Now()
	return s
}

func (l *limiter) openConn(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.get(ip)
	if s.conns >= maxConnsPerIP {
		return false
	}
	s.conns++
	return true
}

func (l *limiter) closeConn(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.get(ip).conns--
}

func (l *limiter) allowOffer(ip, agentID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.get(ip).offers.take(offerBurst, offerEvery) {
		return false
	}
	b := l.agents[agentID]
	if b == nil {
		b = &bucket{}
		l.agents[agentID] = b
	}
	return b.take(agentOfferBurst, agentOfferEvery)
}

func (l *limiter) keyLocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return time.Now().Before(l.get(ip).badUntil)
}

func (l *limiter) badKey(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.get(ip)
	s.badKeys++
	if s.badKeys >= badKeyLimit {
		s.badKeys = 0
		s.badUntil = time.Now().Add(badKeyLockout)
	}
}

// ---- server ----

type peer struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func (p *peer) send(m proto.Msg) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return p.ws.WriteJSON(m)
}

// Server is a running signaling server.
type Server struct {
	cfg      Config
	mu       sync.Mutex
	agents   map[string]*peer
	viewers  map[string]*peer // session -> viewer
	lim      *limiter
	upgrader websocket.Upgrader
}

// originOK allows non-browser clients (agents send no Origin) and the viewer
// page served by this server. It blocks other websites from using a
// visitor's browser to reach a server that's only on their LAN or tailnet.
func (s *Server) originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	for _, a := range strings.Split(s.cfg.AllowOrigin, ",") {
		if a = strings.TrimRight(strings.TrimSpace(a), "/"); a != "" && strings.EqualFold(a, o) {
			return true
		}
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	host := r.Host
	if s.cfg.BehindProxy && r.Header.Get("X-Forwarded-Host") != "" {
		host = r.Header.Get("X-Forwarded-Host")
	}
	return strings.EqualFold(u.Host, host)
}

func (s *Server) iceServers() []proto.ICEServer {
	var out []proto.ICEServer
	if s.cfg.STUN != "" {
		out = append(out, proto.ICEServer{URLs: []string{s.cfg.STUN}})
	}
	if s.cfg.TURN != "" {
		user, pass := s.cfg.TURNUser, s.cfg.TURNPass
		if s.cfg.TURNSecret != "" {
			// coturn "use-auth-secret": username = expiry, password = HMAC-SHA1(secret, username).
			user = fmt.Sprintf("%d:hopdesk", time.Now().Add(time.Hour).Unix())
			m := hmac.New(sha1.New, []byte(s.cfg.TURNSecret))
			m.Write([]byte(user))
			pass = base64.StdEncoding.EncodeToString(m.Sum(nil))
		}
		out = append(out, proto.ICEServer{URLs: []string{s.cfg.TURN}, Username: user, Credential: pass})
	}
	return out
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// Only believe X-Forwarded-For when asked to and when it comes from this machine.
	if s.cfg.BehindProxy && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
				return ip.String()
			}
		}
	}
	return host
}

func keepAlive(ws *websocket.Conn, done <-chan struct{}) {
	_ = ws.SetReadDeadline(time.Now().Add(75 * time.Second))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(75 * time.Second))
	})
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
					return
				}
			}
		}
	}()
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.originOK(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		log.Printf("rejected WebSocket from %s with origin %q", ip, r.Header.Get("Origin"))
		return
	}
	if !s.lim.openConn(ip) {
		http.Error(w, "too many connections", http.StatusTooManyRequests)
		return
	}
	defer s.lim.closeConn(ip)

	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ws.SetReadLimit(64 << 10)
	done := make(chan struct{})
	defer close(done)
	keepAlive(ws, done)

	p := &peer{ws: ws}
	var first proto.Msg
	if err := ws.ReadJSON(&first); err != nil {
		return
	}
	switch first.Type {
	case "register":
		s.runAgent(p, first, ip)
	case "offer":
		s.runViewer(p, first, ip)
	default:
		_ = p.send(proto.Msg{Type: "error", Message: "unknown message"})
	}
}

func (s *Server) runAgent(p *peer, m proto.Msg, ip string) {
	if s.lim.keyLocked(ip) {
		_ = p.send(proto.Msg{Type: "error", Message: "too many wrong agent keys; try again later"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(m.Key), []byte(s.cfg.AgentKey)) != 1 {
		s.lim.badKey(ip)
		log.Printf("agent from %s: bad key", ip)
		_ = p.send(proto.Msg{Type: "error", Message: "bad agent key"})
		return
	}
	id := strings.TrimSpace(m.ID)
	if id == "" || len(id) > 64 {
		_ = p.send(proto.Msg{Type: "error", Message: "invalid id"})
		return
	}
	s.mu.Lock()
	if _, taken := s.agents[id]; taken {
		s.mu.Unlock()
		_ = p.send(proto.Msg{Type: "error", Message: "id already online"})
		return
	}
	s.agents[id] = p
	s.mu.Unlock()
	log.Printf("agent %q online (%s)", id, ip)
	defer func() {
		s.mu.Lock()
		if s.agents[id] == p {
			delete(s.agents, id)
		}
		s.mu.Unlock()
		log.Printf("agent %q offline", id)
	}()

	if err := p.send(proto.Msg{Type: "registered", ICEServers: s.iceServers()}); err != nil {
		return
	}
	for {
		var msg proto.Msg
		if err := p.ws.ReadJSON(&msg); err != nil {
			return
		}
		if msg.Type != "answer" && msg.Type != "error" {
			continue
		}
		s.mu.Lock()
		v := s.viewers[msg.Session]
		s.mu.Unlock()
		if v != nil {
			_ = v.send(proto.Msg{Type: msg.Type, SDP: msg.SDP, Message: msg.Message})
		}
	}
}

func (s *Server) runViewer(p *peer, m proto.Msg, ip string) {
	id := strings.TrimSpace(m.ID)
	if !s.lim.allowOffer(ip, id) {
		_ = p.send(proto.Msg{Type: "error", Message: "too many connection attempts; wait a minute"})
		return
	}
	s.mu.Lock()
	a := s.agents[id]
	s.mu.Unlock()
	if a == nil {
		_ = p.send(proto.Msg{Type: "error", Message: "that device is not online"})
		return
	}
	session := randHex(12)
	s.mu.Lock()
	s.viewers[session] = p
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.viewers, session)
		s.mu.Unlock()
	}()
	if err := a.send(proto.Msg{Type: "offer", Session: session, SDP: m.SDP, From: ip}); err != nil {
		_ = p.send(proto.Msg{Type: "error", Message: "could not reach device"})
		return
	}
	// Wait for the answer, then the viewer closes the socket. Don't hold it forever.
	_ = p.ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	for {
		if _, _, err := p.ws.ReadMessage(); err != nil {
			return
		}
	}
}

// securityHeaders protects the viewer page.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' blob: data:; media-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// New validates the config and creates a server. Its background work stops when ctx ends.
func New(ctx context.Context, cfg Config) (*Server, error) {
	if len(cfg.AgentKey) < 16 {
		return nil, errors.New("agent key must be at least 16 characters")
	}
	if cfg.TURN != "" && cfg.TURNSecret == "" {
		log.Printf("warning: static TURN credentials are given to every visitor; use a TURN secret for short-lived ones")
	}
	s := &Server{cfg: cfg, agents: map[string]*peer{}, viewers: map[string]*peer{}, lim: newLimiter(ctx)}
	s.upgrader = websocket.Upgrader{ReadBufferSize: 16 << 10, WriteBufferSize: 16 << 10, CheckOrigin: s.originOK}
	return s, nil
}

// Handler returns the HTTP handler: viewer page, /ws and /config.
func (s *Server) Handler() http.Handler {
	sub, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("/", securityHeaders(http.FileServer(http.FS(sub))))
	mux.HandleFunc("/ws", s.serveWS)
	mux.Handle("/config", securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"iceServers": s.iceServers()})
	})))
	return mux
}

// Run serves until ctx is cancelled or the listener fails.
func Run(ctx context.Context, cfg Config) error {
	s, err := New(ctx, cfg)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if cfg.TLSCert != "" {
		log.Printf("hopdesk server on https://%s", cfg.Addr)
		err = srv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
	} else {
		log.Printf("hopdesk server on http://%s", cfg.Addr)
		log.Printf("the viewer needs HTTPS (or localhost): use TLS, a reverse proxy, or `tailscale serve`")
		err = srv.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
