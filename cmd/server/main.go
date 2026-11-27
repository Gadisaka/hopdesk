// Command server is the hopdesk signaling server. It lets agents register by
// ID, passes the WebRTC offer/answer between a viewer and an agent, and serves
// the browser viewer. Screen data never goes through it, and it never sees
// passwords: authentication happens end to end between viewer and agent.
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"hopdesk/internal/proto"
)

//go:embed web
var webFS embed.FS

var (
	addr       = flag.String("addr", ":8080", "listen address")
	agentKey   = flag.String("agent-key", "", "secret agents must present to register (better: env HOPDESK_AGENT_KEY; generated if empty)")
	stunURL    = flag.String("stun", "stun:stun.l.google.com:19302", "STUN server URL (empty to disable)")
	turnURL    = flag.String("turn", "", "TURN server URL, e.g. turn:turn.example.com:3478 (optional)")
	turnSecret = flag.String("turn-secret", "", "coturn static-auth-secret, for short-lived TURN credentials (better: env HOPDESK_TURN_SECRET)")
	turnUser   = flag.String("turn-user", "", "static TURN username (prefer -turn-secret)")
	turnPass   = flag.String("turn-pass", "", "static TURN password (prefer -turn-secret)")
	tlsCert    = flag.String("tls-cert", "", "TLS certificate file (serve HTTPS/WSS)")
	tlsKey     = flag.String("tls-key", "", "TLS key file")
	proxied    = flag.Bool("behind-proxy", false, "trust X-Forwarded-For/-Host from a reverse proxy on this machine (e.g. tailscale serve, Caddy)")
	allowOrig  = flag.String("allow-origin", "", "extra viewer origins to accept, comma-separated (e.g. https://mymac.tail1234.ts.net)")
)

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

func newLimiter() *limiter {
	l := &limiter{ips: map[string]*ipState{}, agents: map[string]*bucket{}}
	go func() {
		for range time.Tick(time.Minute) {
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

// ---- WebSocket plumbing ----

// originOK allows non-browser clients (agents send no Origin) and the viewer
// page served by this server. It blocks other websites from using a
// visitor's browser to reach a server that's only on their LAN or tailnet.
func originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	for _, a := range strings.Split(*allowOrig, ",") {
		if a = strings.TrimRight(strings.TrimSpace(a), "/"); a != "" && strings.EqualFold(a, o) {
			return true
		}
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	host := r.Host
	if *proxied && r.Header.Get("X-Forwarded-Host") != "" {
		host = r.Header.Get("X-Forwarded-Host")
	}
	return strings.EqualFold(u.Host, host)
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  16 << 10,
	WriteBufferSize: 16 << 10,
	CheckOrigin:     originOK,
}

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

type hub struct {
	mu      sync.Mutex
	agents  map[string]*peer
	viewers map[string]*peer // session -> viewer
	lim     *limiter
}

func iceServers() []proto.ICEServer {
	var s []proto.ICEServer
	if *stunURL != "" {
		s = append(s, proto.ICEServer{URLs: []string{*stunURL}})
	}
	if *turnURL != "" {
		user, pass := *turnUser, *turnPass
		if *turnSecret != "" {
			// coturn "use-auth-secret": username = expiry, password = HMAC-SHA1(secret, username).
			user = fmt.Sprintf("%d:hopdesk", time.Now().Add(time.Hour).Unix())
			m := hmac.New(sha1.New, []byte(*turnSecret))
			m.Write([]byte(user))
			pass = base64.StdEncoding.EncodeToString(m.Sum(nil))
		}
		s = append(s, proto.ICEServer{URLs: []string{*turnURL}, Username: user, Credential: pass})
	}
	return s
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// Only believe X-Forwarded-For when asked to and when it comes from this machine.
	if *proxied && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback() {
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

func (h *hub) serveWS(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !originOK(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		log.Printf("rejected WebSocket from %s with origin %q", ip, r.Header.Get("Origin"))
		return
	}
	if !h.lim.openConn(ip) {
		http.Error(w, "too many connections", http.StatusTooManyRequests)
		return
	}
	defer h.lim.closeConn(ip)

	ws, err := upgrader.Upgrade(w, r, nil)
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
		h.runAgent(p, first, ip)
	case "offer":
		h.runViewer(p, first, ip)
	default:
		_ = p.send(proto.Msg{Type: "error", Message: "unknown message"})
	}
}

func (h *hub) runAgent(p *peer, m proto.Msg, ip string) {
	if h.lim.keyLocked(ip) {
		_ = p.send(proto.Msg{Type: "error", Message: "too many wrong agent keys; try again later"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(m.Key), []byte(*agentKey)) != 1 {
		h.lim.badKey(ip)
		log.Printf("agent from %s: bad key", ip)
		_ = p.send(proto.Msg{Type: "error", Message: "bad agent key"})
		return
	}
	id := strings.TrimSpace(m.ID)
	if id == "" || len(id) > 64 {
		_ = p.send(proto.Msg{Type: "error", Message: "invalid id"})
		return
	}
	h.mu.Lock()
	if _, taken := h.agents[id]; taken {
		h.mu.Unlock()
		_ = p.send(proto.Msg{Type: "error", Message: "id already online"})
		return
	}
	h.agents[id] = p
	h.mu.Unlock()
	log.Printf("agent %q online (%s)", id, ip)
	defer func() {
		h.mu.Lock()
		if h.agents[id] == p {
			delete(h.agents, id)
		}
		h.mu.Unlock()
		log.Printf("agent %q offline", id)
	}()

	if err := p.send(proto.Msg{Type: "registered", ICEServers: iceServers()}); err != nil {
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
		h.mu.Lock()
		v := h.viewers[msg.Session]
		h.mu.Unlock()
		if v != nil {
			_ = v.send(proto.Msg{Type: msg.Type, SDP: msg.SDP, Message: msg.Message})
		}
	}
}

func (h *hub) runViewer(p *peer, m proto.Msg, ip string) {
	id := strings.TrimSpace(m.ID)
	if !h.lim.allowOffer(ip, id) {
		_ = p.send(proto.Msg{Type: "error", Message: "too many connection attempts; wait a minute"})
		return
	}
	h.mu.Lock()
	a := h.agents[id]
	h.mu.Unlock()
	if a == nil {
		_ = p.send(proto.Msg{Type: "error", Message: "that device is not online"})
		return
	}
	session := randHex(12)
	h.mu.Lock()
	h.viewers[session] = p
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.viewers, session)
		h.mu.Unlock()
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

func secretFromEnv(flagVal *string, flagName, env string) {
	if *flagVal != "" {
		log.Printf("note: -%s on the command line is visible to other users (ps) and in shell history; prefer env %s", flagName, env)
		return
	}
	*flagVal = os.Getenv(env)
}

func main() {
	flag.Parse()
	secretFromEnv(agentKey, "agent-key", "HOPDESK_AGENT_KEY")
	secretFromEnv(turnSecret, "turn-secret", "HOPDESK_TURN_SECRET")
	if *agentKey == "" {
		*agentKey = randHex(16)
		fmt.Fprintf(os.Stderr, "\nNo agent key set. Generated one for this run (set HOPDESK_AGENT_KEY to keep it):\n  %s\n\n", *agentKey)
	} else if len(*agentKey) < 16 {
		log.Fatal("agent key must be at least 16 characters")
	}
	if *turnURL != "" && *turnSecret == "" {
		log.Printf("warning: static TURN credentials are given to every visitor; use -turn-secret for short-lived ones")
	}

	h := &hub{agents: map[string]*peer{}, viewers: map[string]*peer{}, lim: newLimiter()}
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", securityHeaders(http.FileServer(http.FS(sub))))
	mux.HandleFunc("/ws", h.serveWS)
	mux.Handle("/config", securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"iceServers": iceServers()})
	})))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	if *tlsCert != "" {
		log.Printf("hopdesk server on https://%s", *addr)
		log.Fatal(srv.ListenAndServeTLS(*tlsCert, *tlsKey))
	}
	log.Printf("hopdesk server on http://%s", *addr)
	log.Printf("the viewer needs HTTPS (or localhost): use -tls-cert/-tls-key, a reverse proxy, or `tailscale serve`")
	log.Fatal(srv.ListenAndServe())
}
