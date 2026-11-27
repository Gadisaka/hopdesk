// Command agent runs on the computer you want to control. It registers with
// the hopdesk server, answers WebRTC offers from viewers, checks the password
// over the encrypted data channel, then streams the screen (H.264 via ffmpeg,
// or JPEG frames as a fallback), replays input, syncs the clipboard and
// transfers files.
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
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"

	"hopdesk/internal/proto"
)

var (
	serverURL   = flag.String("server", "ws://localhost:8080/ws", "signaling server WebSocket URL (wss:// with TLS)")
	agentID     = flag.String("id", "", "device ID viewers type to reach this computer (default: hostname)")
	password    = flag.String("password", "", "connection password (default: random, printed at start)")
	agentKey    = flag.String("key", "", "agent key printed by the server")
	defFPS      = flag.Int("fps", 15, "starting frames per second (5, 10, 15, 20 or 30)")
	defQuality  = flag.String("quality", "medium", "starting quality: low, medium or high")
	defDisplay  = flag.Int("display", 0, "display to show first")
	codecFlag   = flag.String("codec", "auto", "auto, h264 or jpeg (h264 needs ffmpeg)")
	ffmpegPath  = flag.String("ffmpeg", "ffmpeg", "path to the ffmpeg program")
	encoderFlag = flag.String("encoder", "", "ffmpeg H.264 encoder (default: best available, e.g. h264_videotoolbox or libx264)")
	uploadDir   = flag.String("upload-dir", "", "folder for files sent from the viewer (default: ~/Downloads)")
)

// Quality presets: max frame width, JPEG quality, H.264 bitrate.
type preset struct {
	maxW    int
	jpegQ   int
	bitrate int
}

var presets = map[string]preset{
	"low":    {1280, 45, 800_000},
	"medium": {1600, 65, 2_000_000},
	"high":   {2560, 80, 5_000_000},
}

var allowedFPS = map[int]bool{5: true, 10: true, 15: true, 20: true, 30: true}

type settings struct {
	Display int    `json:"display"`
	Quality string `json:"quality"`
	FPS     int    `json:"fps"`
}

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
	track  *webrtc.TrackLocalStaticRTP
	rtp    rtpState // only touched by the single running H.264 forwarder

	mu         sync.Mutex
	mode       string // "h264" or "jpeg"
	authed     bool
	started    bool
	frames     *webrtc.DataChannel
	framesOpen bool
	ctrl       *webrtc.DataChannel
	set        settings
	setVer     int
	clipLast   string

	ctrlMu sync.Mutex
}

func (s *session) short() string { return s.id[:6] }

func (s *session) isAuthed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authed
}

func (s *session) getSettings() (settings, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set, s.setVer
}

func (s *session) getMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

func (s *session) sendCtrl(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.mu.Lock()
	dc := s.ctrl
	s.mu.Unlock()
	if dc == nil {
		return
	}
	s.ctrlMu.Lock()
	defer s.ctrlMu.Unlock()
	_ = dc.SendText(string(b))
}

func (s *session) sendHello() {
	set, _ := s.getSettings()
	type disp struct {
		W int `json:"w"`
		H int `json:"h"`
	}
	ds := []disp{}
	for _, b := range listDisplays() {
		ds = append(ds, disp{b.Dx(), b.Dy()})
	}
	home, _ := os.UserHomeDir()
	s.sendCtrl(map[string]any{
		"t": "hello", "mode": s.getMode(), "displays": ds, "settings": set,
		"home": home, "os": runtime.GOOS, "id": *agentID,
	})
}

func (s *session) maybeStart() {
	s.mu.Lock()
	ready := s.authed && !s.started && (s.mode == "h264" || (s.framesOpen && s.frames != nil))
	if ready {
		s.started = true
	}
	mode, frames := s.mode, s.frames
	s.mu.Unlock()
	if !ready {
		return
	}
	if mode == "h264" {
		go runH264(s)
	} else {
		go runJPEG(s, frames)
	}
}

// fallbackToJPEG switches a session to JPEG frames when the H.264 encoder keeps failing.
func (s *session) fallbackToJPEG() {
	s.mu.Lock()
	s.mode = "jpeg"
	frames, open := s.frames, s.framesOpen
	s.mu.Unlock()
	s.sendHello()
	if frames != nil && open {
		go runJPEG(s, frames)
	}
}

func (s *session) close() {
	s.cancel()
	_ = s.pc.Close()
}

type ctrlMsg struct {
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
	Text     string  `json:"text"`
	Path     string  `json:"path"`
	Req      int     `json:"req"`
	Display  int     `json:"display"`
	Quality  string  `json:"quality"`
	FPS      int     `json:"fps"`
}

func (s *session) handleCtrl(m ctrlMsg) {
	if m.T == "auth" {
		if s.isAuthed() {
			return
		}
		ok, locked := checkPassword(m.Password)
		s.sendCtrl(map[string]any{"t": "auth", "ok": ok, "locked": locked})
		if !ok {
			log.Printf("session %s: wrong password", s.short())
			time.AfterFunc(time.Second, s.close)
			return
		}
		log.Printf("session %s: viewer authenticated (%s video)", s.short(), s.getMode())
		s.mu.Lock()
		s.authed = true
		s.mu.Unlock()
		s.sendHello()
		go watchClipboard(s)
		s.maybeStart()
		return
	}
	if !s.isAuthed() {
		return
	}
	switch m.T {
	case "move", "down", "up", "wheel", "key":
		s.handleInput(m)
	case "settings":
		s.applySettings(m)
	case "clip":
		setClipboard(s, m.Text)
	case "ls":
		go listDir(s, m.Path, m.Req)
	}
}

func (s *session) applySettings(m ctrlMsg) {
	n := len(listDisplays())
	s.mu.Lock()
	next := s.set
	if m.Display >= 0 && m.Display < n {
		next.Display = m.Display
	}
	if _, ok := presets[m.Quality]; ok {
		next.Quality = m.Quality
	}
	if allowedFPS[m.FPS] {
		next.FPS = m.FPS
	}
	changed := next != s.set
	if changed {
		s.set = next
		s.setVer++
	}
	s.mu.Unlock()
	if changed {
		log.Printf("session %s: display %d, %s quality, %d fps", s.short(), next.Display+1, next.Quality, next.FPS)
	}
	s.sendHello()
}

func (s *session) handleInput(m ctrlMsg) {
	set, _ := s.getSettings()
	b := displayBounds(set.Display)
	px := float64(b.Min.X) + m.X*float64(b.Dx())
	py := float64(b.Min.Y) + m.Y*float64(b.Dy())
	switch m.T {
	case "move":
		inputMove(px, py)
	case "down", "up":
		inputButton(px, py, m.B, m.T == "down", m.N)
	case "wheel":
		inputScroll(m.DX, m.DY)
	case "key":
		inputKey(m.Code, m.Key, m.Down)
	}
}

func handleOffer(sig *sigConn, ice []webrtc.ICEServer, m proto.Msg) {
	fail := func(msg string) { sig.send(proto.Msg{Type: "error", Session: m.Session, Message: msg}) }

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		fail("agent error")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{id: m.Session, pc: pc, ctx: ctx, cancel: cancel, mode: "jpeg"}
	s.set = settings{Display: 0, Quality: "medium", FPS: 15}
	if *defDisplay >= 0 && *defDisplay < len(listDisplays()) {
		s.set.Display = *defDisplay
	}
	if _, ok := presets[*defQuality]; ok {
		s.set.Quality = *defQuality
	}
	if allowedFPS[*defFPS] {
		s.set.FPS = *defFPS
	}

	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		log.Printf("session %s: %s", s.short(), st)
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
				s.framesOpen = true
				s.mu.Unlock()
				s.maybeStart()
			})
		case "input":
			s.mu.Lock()
			s.ctrl = dc
			s.mu.Unlock()
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				var cm ctrlMsg
				if json.Unmarshal(msg.Data, &cm) == nil {
					s.handleCtrl(cm)
				}
			})
		case "upload":
			handleUpload(s, dc)
		case "download":
			handleDownload(s, dc)
		default:
			_ = dc.Close()
		}
	})

	// Drop sessions that never authenticate.
	time.AfterFunc(20*time.Second, func() {
		if !s.isAuthed() {
			s.close()
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: m.SDP}); err != nil {
		s.close()
		fail("bad offer")
		return
	}

	// Use H.264 when ffmpeg has an encoder and the viewer asked for video.
	if *codecFlag != "jpeg" && strings.Contains(m.SDP, "m=video") && h264Encoder() != "" {
		track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		}, "video", "hopdesk")
		if err == nil {
			if sender, err := pc.AddTrack(track); err == nil {
				s.track = track
				s.mode = "h264"
				go func() { // read RTCP so pion's interceptors keep working
					buf := make([]byte, 1500)
					for {
						if _, _, err := sender.Read(buf); err != nil {
							return
						}
					}
				}()
			}
		}
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		s.close()
		fail("agent error")
		return
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		s.close()
		fail("agent error")
		return
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
	}
	sig.send(proto.Msg{Type: "answer", Session: m.Session, SDP: pc.LocalDescription().SDP})
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
	if *codecFlag != "jpeg" {
		h264Encoder() // detect now so the log shows which video mode is used
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
