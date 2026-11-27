// Command agent runs on the computer you want to control. It registers with
// the hopdesk server, answers WebRTC offers from viewers, checks the password
// over the encrypted data channel, then streams the screen and replays input.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"

	"hopdesk/internal/proto"
)

var (
	serverURL = flag.String("server", "ws://localhost:8080/ws", "signaling server WebSocket URL (wss:// with TLS)")
	agentID   = flag.String("id", "", "device ID viewers type to reach this computer (default: hostname)")
	password  = flag.String("password", "", "connection password (default: random, printed at start)")
	agentKey  = flag.String("key", "", "agent key printed by the server")
	fps       = flag.Int("fps", 10, "max frames per second")
	quality   = flag.Int("quality", 60, "JPEG quality 1-100")
	maxWidth  = flag.Int("maxwidth", 1600, "downscale frames wider than this (0 = never)")
	display   = flag.Int("display", 0, "display index to share")
)

// ---- signaling connection ----

type sigConn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func (s *sigConn) send(m proto.Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := s.ws.WriteJSON(m); err != nil {
		log.Printf("signaling write: %v", err)
	}
}

// ---- password lockout ----

var lock struct {
	sync.Mutex
	failures int
	until    time.Time
}

// checkPassword returns (ok, locked).
func checkPassword(p string) (bool, bool) {
	lock.Lock()
	defer lock.Unlock()
	if time.Now().Before(lock.until) {
		return false, true
	}
	if subtle.ConstantTimeCompare([]byte(p), []byte(*password)) == 1 {
		lock.failures = 0
		return true, false
	}
	lock.failures++
	if lock.failures >= 5 {
		lock.failures = 0
		lock.until = time.Now().Add(time.Minute)
		log.Printf("5 wrong passwords: refusing connections for 1 minute")
	}
	return false, false
}

// ---- sessions ----

type session struct {
	id     string
	pc     *webrtc.PeerConnection
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	authed  bool
	frames  *webrtc.DataChannel
	open    bool
	started bool
}

func (s *session) maybeStart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authed && s.open && s.frames != nil && !s.started {
		s.started = true
		go streamFrames(s.ctx, s.frames)
	}
}

func (s *session) close() {
	s.cancel()
	_ = s.pc.Close()
}

type inputEvent struct {
	T        string  `json:"t"`
	Password string  `json:"password"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	B        int     `json:"b"`
	N        int     `json:"n"`
	DX       float64 `json:"dx"`
	DY       float64 `json:"dy"`
	Code     string  `json:"code"`
	Key      string  `json:"key"`
	Down     bool    `json:"down"`
}

func handleOffer(sig *sigConn, ice []webrtc.ICEServer, m proto.Msg) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		sig.send(proto.Msg{Type: "error", Session: m.Session, Message: "agent error"})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{id: m.Session, pc: pc, ctx: ctx, cancel: cancel}

	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		log.Printf("session %s: %s", s.id[:6], st)
		switch st {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			go s.close() // not inside the callback, to avoid blocking pion
		}
	})

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		switch dc.Label() {
		case "frames":
			s.mu.Lock()
			s.frames = dc
			s.mu.Unlock()
			dc.OnOpen(func() {
				s.mu.Lock()
				s.open = true
				s.mu.Unlock()
				s.maybeStart()
			})
		case "input":
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				var ev inputEvent
				if json.Unmarshal(msg.Data, &ev) != nil {
					return
				}
				s.mu.Lock()
				authed := s.authed
				s.mu.Unlock()
				if ev.T == "auth" {
					ok, locked := checkPassword(ev.Password)
					reply, _ := json.Marshal(map[string]any{"t": "auth", "ok": ok, "locked": locked})
					_ = dc.Send(reply)
					if !ok {
						log.Printf("session %s: wrong password", s.id[:6])
						time.AfterFunc(time.Second, s.close)
						return
					}
					log.Printf("session %s: viewer authenticated", s.id[:6])
					s.mu.Lock()
					s.authed = true
					s.mu.Unlock()
					s.maybeStart()
					return
				}
				if authed {
					handleInput(ev)
				}
			})
		default:
			_ = dc.Close()
		}
	})

	// Drop sessions that never authenticate.
	time.AfterFunc(20*time.Second, func() {
		s.mu.Lock()
		authed := s.authed
		s.mu.Unlock()
		if !authed {
			s.close()
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: m.SDP}); err != nil {
		s.close()
		sig.send(proto.Msg{Type: "error", Session: m.Session, Message: "bad offer"})
		return
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		s.close()
		sig.send(proto.Msg{Type: "error", Session: m.Session, Message: "agent error"})
		return
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		s.close()
		sig.send(proto.Msg{Type: "error", Session: m.Session, Message: "agent error"})
		return
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
	}
	sig.send(proto.Msg{Type: "answer", Session: m.Session, SDP: pc.LocalDescription().SDP})
}

func handleInput(ev inputEvent) {
	b := displayBounds()
	px := float64(b.Min.X) + ev.X*float64(b.Dx())
	py := float64(b.Min.Y) + ev.Y*float64(b.Dy())
	switch ev.T {
	case "move":
		inputMove(px, py)
	case "down", "up":
		inputButton(px, py, ev.B, ev.T == "down", ev.N)
	case "wheel":
		inputScroll(ev.DX, ev.DY)
	case "key":
		inputKey(ev.Code, ev.Key, ev.Down)
	}
}

// ---- main loop ----

func toPionICE(s []proto.ICEServer) []webrtc.ICEServer {
	out := make([]webrtc.ICEServer, 0, len(s))
	for _, x := range s {
		out = append(out, webrtc.ICEServer{URLs: x.URLs, Username: x.Username, Credential: x.Credential})
	}
	return out
}

func runOnce() error {
	ws, _, err := websocket.DefaultDialer.Dial(*serverURL, nil)
	if err != nil {
		return err
	}
	defer ws.Close()
	sig := &sigConn{ws: ws}
	sig.send(proto.Msg{Type: "register", ID: *agentID, Key: *agentKey})

	var ice []webrtc.ICEServer
	for {
		var m proto.Msg
		if err := ws.ReadJSON(&m); err != nil {
			return err
		}
		switch m.Type {
		case "registered":
			ice = toPionICE(m.ICEServers)
			log.Printf("online as %q. Viewers connect with this ID and the password.", *agentID)
		case "offer":
			go handleOffer(sig, ice, m)
		case "error":
			return fmt.Errorf("server: %s", m.Message)
		}
	}
}

func randomPassword() string {
	const chars = "abcdefghjkmnpqrstuvwxyz23456789"
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		sb.WriteByte(chars[n.Int64()])
	}
	return sb.String()
}

func main() {
	flag.Parse()
	if *agentKey == "" {
		log.Fatal("-key is required (the server prints it when it starts)")
	}
	if *agentID == "" {
		h, _ := os.Hostname()
		*agentID = strings.ToLower(strings.TrimSuffix(h, ".local"))
	}
	if *password == "" {
		*password = randomPassword()
		log.Printf("no -password given; using this one for this run: %s", *password)
	} else if len(*password) < 8 {
		log.Fatal("-password must be at least 8 characters")
	}
	if err := platformInit(); err != nil {
		log.Printf("warning: %v", err)
	}
	if err := initCapture(); err != nil {
		log.Fatalf("screen capture: %v", err)
	}

	backoff := 2 * time.Second
	for {
		start := time.Now()
		err := runOnce()
		log.Printf("disconnected from server: %v", err)
		if time.Since(start) > time.Minute {
			backoff = 2 * time.Second
		}
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}
