// Command server is the hopdesk signaling server. It lets agents register by
// ID, passes the WebRTC offer/answer between a viewer and an agent, and serves
// the browser viewer. Screen data never goes through it.
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"hopdesk/internal/proto"
)

//go:embed web
var webFS embed.FS

var (
	addr     = flag.String("addr", ":8080", "listen address")
	agentKey = flag.String("agent-key", "", "secret agents must present to register (generated if empty)")
	stunURL  = flag.String("stun", "stun:stun.l.google.com:19302", "STUN server URL (empty to disable)")
	turnURL  = flag.String("turn", "", "TURN server URL, e.g. turn:turn.example.com:3478 (optional)")
	turnUser = flag.String("turn-user", "", "TURN username")
	turnPass = flag.String("turn-pass", "", "TURN password")
	tlsCert  = flag.String("tls-cert", "", "TLS certificate file (serve HTTPS/WSS)")
	tlsKey   = flag.String("tls-key", "", "TLS key file")
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  16 << 10,
	WriteBufferSize: 16 << 10,
	// Agents are not browsers, and viewers authenticate to the agent
	// end-to-end, so the origin check adds nothing here.
	CheckOrigin: func(r *http.Request) bool { return true },
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
}

func iceServers() []proto.ICEServer {
	var s []proto.ICEServer
	if *stunURL != "" {
		s = append(s, proto.ICEServer{URLs: []string{*stunURL}})
	}
	if *turnURL != "" {
		s = append(s, proto.ICEServer{URLs: []string{*turnURL}, Username: *turnUser, Credential: *turnPass})
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
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ws.SetReadLimit(1 << 20)
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
		h.runAgent(p, first, r.RemoteAddr)
	case "offer":
		h.runViewer(p, first)
	default:
		_ = p.send(proto.Msg{Type: "error", Message: "unknown message"})
	}
}

func (h *hub) runAgent(p *peer, m proto.Msg, remote string) {
	if subtle.ConstantTimeCompare([]byte(m.Key), []byte(*agentKey)) != 1 {
		log.Printf("agent from %s: bad key", remote)
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
	log.Printf("agent %q online (%s)", id, remote)
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

func (h *hub) runViewer(p *peer, m proto.Msg) {
	h.mu.Lock()
	a := h.agents[strings.TrimSpace(m.ID)]
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
	if err := a.send(proto.Msg{Type: "offer", Session: session, SDP: m.SDP}); err != nil {
		_ = p.send(proto.Msg{Type: "error", Message: "could not reach device"})
		return
	}
	// Keep the socket open until the viewer closes it (after the answer).
	for {
		if _, _, err := p.ws.ReadMessage(); err != nil {
			return
		}
	}
}

func main() {
	flag.Parse()
	if *agentKey == "" {
		*agentKey = randHex(16)
		log.Printf("no -agent-key given; generated one for this run: %s", *agentKey)
	}
	h := &hub{agents: map[string]*peer{}, viewers: map[string]*peer{}}

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/ws", h.serveWS)
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"iceServers": iceServers()})
	})

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if *tlsCert != "" {
		log.Printf("hopdesk server on https://%s", *addr)
		log.Fatal(srv.ListenAndServeTLS(*tlsCert, *tlsKey))
	}
	log.Printf("hopdesk server on http://%s (use -tls-cert/-tls-key or Tailscale outside your LAN)", *addr)
	log.Fatal(srv.ListenAndServe())
}
