// Command agent runs on the computer you want to control. It registers with
// the hopdesk server and answers WebRTC offers from viewers. Over the
// encrypted connection it proves its identity and checks the viewer's
// password proof (see internal/auth), then streams the screen (H.264 via
// ffmpeg, or JPEG frames), replays input, syncs the clipboard and transfers files.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"

	"hopdesk/internal/auth"
	"hopdesk/internal/proto"
)

var (
	serverURL   = flag.String("server", "ws://localhost:8080/ws", "signaling server URL: wss:// for anything outside your LAN/tailnet")
	agentID     = flag.String("id", "", "device ID viewers type to reach this computer (default: hostname)")
	password    = flag.String("password", "", "connection password (better: env HOPDESK_PASSWORD; default: generate and save one)")
	resetPass   = flag.Bool("reset-password", false, "generate a new password (and forget the saved one)")
	agentKey    = flag.String("key", "", "agent key from the server (better: env HOPDESK_KEY)")
	stateFile   = flag.String("state", "", "where to keep this agent's identity (default: user config folder)")
	insecureWS  = flag.Bool("insecure-ws", false, "allow plain ws:// to a public address (not recommended)")
	confirmFlag = flag.Bool("confirm", false, "ask the person at this computer to approve each connection")
	idleTimeout = flag.Duration("idle", 30*time.Minute, "disconnect viewers after this long without input (0 = never)")
	clipMode    = flag.String("clipboard", "both", "clipboard sync: both, to-remote (viewer -> this computer only) or off")
	defFPS      = flag.Int("fps", 15, "starting frames per second (5, 10, 15, 20 or 30)")
	defQuality  = flag.String("quality", "medium", "starting quality: low, medium or high")
	defDisplay  = flag.Int("display", 0, "display to show first")
	codecFlag   = flag.String("codec", "auto", "auto, h264 or jpeg (h264 needs ffmpeg)")
	ffmpegPath  = flag.String("ffmpeg", "ffmpeg", "path to the ffmpeg program")
	encoderFlag = flag.String("encoder", "", "ffmpeg H.264 encoder (default: best available, e.g. h264_videotoolbox or libx264)")
	uploadDir   = flag.String("upload-dir", "", "folder for files sent from the viewer (default: ~/Downloads)")
)

// Identity and password key, set up in main.
var (
	identity ed25519.PrivateKey
	pubB64   string
	pwKey    []byte
	pwSalt   string
	pwIter   int
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

// ---- sessions ----

type session struct {
	id     string
	from   string // viewer's IP as seen by the server
	pc     *webrtc.PeerConnection
	ctx    context.Context
	cancel context.CancelFunc
	track  *webrtc.TrackLocalStaticRTP
	rtp    rtpState // only touched by the single running H.264 forwarder

	closeOnce  sync.Once
	lastActive atomic.Int64

	mu         sync.Mutex
	mode       string // "h264" or "jpeg"
	nonceV     string
	nonceA     string
	binding    string
	auth1Done  bool
	auth2Done  bool
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
		"home": home, "os": runtime.GOOS, "id": *agentID, "clipboard": *clipMode,
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
	s.closeOnce.Do(func() {
		s.cancel()
		_ = s.pc.Close()
		authed := s.isAuthed()
		releaseSession(!authed)
		if authed {
			log.Printf("session %s: ended", s.short())
			notify("hopdesk", "Remote session from "+s.from+" ended")
		}
	})
}

type ctrlMsg struct {
	T       string  `json:"t"`
	Nonce   string  `json:"nonce"`
	Proof   string  `json:"proof"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	B       int     `json:"b"`
	N       int     `json:"n"`
	DX      float64 `json:"dx"`
	DY      float64 `json:"dy"`
	Code    string  `json:"code"`
	Key     string  `json:"key"`
	Down    bool    `json:"down"`
	Text    string  `json:"text"`
	Path    string  `json:"path"`
	Req     int     `json:"req"`
	Display int     `json:"display"`
	Quality string  `json:"quality"`
	FPS     int     `json:"fps"`
}

func randB64(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// reject tells the viewer why and closes the connection shortly after.
func (s *session) reject(reason string) {
	s.sendCtrl(map[string]any{"t": "auth", "ok": false, "reason": reason})
	time.AfterFunc(time.Second, s.close)
}

func (s *session) handleCtrl(m ctrlMsg) {
	switch m.T {
	case "auth1":
		s.authStep1(m)
		return
	case "auth2":
		s.authStep2(m)
		return
	}
	if !s.isAuthed() {
		return
	}
	s.lastActive.Store(time.Now().UnixNano())
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

// authStep1: prove who we are, bound to this connection.
func (s *session) authStep1(m ctrlMsg) {
	s.mu.Lock()
	if s.auth1Done || len(m.Nonce) < 16 || len(m.Nonce) > 128 {
		s.mu.Unlock()
		return
	}
	s.auth1Done = true
	s.mu.Unlock()

	if !guard.check(s.from) {
		log.Printf("session %s: refused, too many wrong passwords from %s", s.short(), s.from)
		s.reject("locked")
		return
	}
	local, remote := s.pc.LocalDescription(), s.pc.RemoteDescription()
	if local == nil || remote == nil {
		s.reject("error")
		return
	}
	binding, err := auth.Binding(remote.SDP, local.SDP)
	if err != nil {
		s.reject("error")
		return
	}
	nA := randB64(18)
	s.mu.Lock()
	s.nonceV, s.nonceA, s.binding = m.Nonce, nA, binding
	s.mu.Unlock()
	s.sendCtrl(map[string]any{
		"t": "auth1", "pub": pubB64, "nonce": nA, "salt": pwSalt, "iter": pwIter,
		"sig": base64.StdEncoding.EncodeToString(auth.SignAgent(identity, binding, m.Nonce)),
	})
}

// authStep2: check the viewer's password proof. One attempt per connection.
func (s *session) authStep2(m ctrlMsg) {
	s.mu.Lock()
	if !s.auth1Done || s.auth2Done || s.binding == "" {
		s.mu.Unlock()
		return
	}
	s.auth2Done = true
	binding, nV, nA := s.binding, s.nonceV, s.nonceA
	s.mu.Unlock()

	proof, err := base64.StdEncoding.DecodeString(m.Proof)
	if err != nil || !auth.VerifyProof(pwKey, binding, nV, nA, proof) {
		guard.fail(s.from)
		log.Printf("session %s: wrong password from %s", s.short(), s.from)
		s.reject("password")
		return
	}
	guard.success(s.from)
	if *confirmFlag {
		go func() {
			log.Printf("session %s: password OK, waiting for local approval", s.short())
			if !askApproval(fmt.Sprintf("Allow a remote connection from %s?\n\nThey will be able to see this screen, control it, and access files.", s.from)) {
				log.Printf("session %s: declined at this computer", s.short())
				s.reject("declined")
				return
			}
			s.finishAuth()
		}()
		return
	}
	s.finishAuth()
}

func (s *session) finishAuth() {
	// Check and set under the lock so close() can't race us on the session counters.
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	s.authed = true
	promoteSession()
	s.mu.Unlock()
	s.lastActive.Store(time.Now().UnixNano())
	log.Printf("session %s: viewer from %s connected (%s video)", s.short(), s.from, s.getMode())
	notify("hopdesk", "Remote session started from "+s.from)
	s.sendCtrl(map[string]any{"t": "auth", "ok": true})
	s.sendHello()
	if *clipMode == "both" {
		go watchClipboard(s)
	}
	if *idleTimeout > 0 {
		go s.watchIdle()
	}
	s.maybeStart()
}

func (s *session) watchIdle() {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		if time.Since(time.Unix(0, s.lastActive.Load())) > *idleTimeout {
			log.Printf("session %s: idle for %s, disconnecting", s.short(), *idleTimeout)
			s.sendCtrl(map[string]any{"t": "bye", "reason": "idle"})
			time.AfterFunc(time.Second, s.close)
			return
		}
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
	if len(m.Session) < 12 {
		return
	}
	if !reserveSession() {
		fail("device is busy; try again shortly")
		return
	}
	from := m.From
	if from == "" {
		from = "unknown"
	}

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		releaseSession(true)
		fail("agent error")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{id: m.Session, from: from, pc: pc, ctx: ctx, cancel: cancel, mode: "jpeg"}
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
			first := s.ctrl == nil
			if first {
				s.ctrl = dc
			}
			s.mu.Unlock()
			if !first {
				_ = dc.Close()
				return
			}
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				var cm ctrlMsg
				if len(msg.Data) <= 64<<10 && json.Unmarshal(msg.Data, &cm) == nil {
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

	// Drop connections that don't finish authenticating (allowing for -confirm's dialog).
	limit := 20 * time.Second
	if *confirmFlag {
		limit = 60 * time.Second
	}
	time.AfterFunc(limit, func() {
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
	ws.SetReadLimit(256 << 10)
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
			log.Printf("online as %q", *agentID)
		case "offer":
			go handleOffer(sig, ice, m)
		case "error":
			return fmt.Errorf("server: %s", m.Message)
		}
	}
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)} // Tailscale range

func isPrivate(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip)
}

// checkServerURL refuses plain ws:// to public addresses, where the agent key
// and connection setup would cross the internet unencrypted.
func checkServerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "wss":
		return nil
	case "ws":
	default:
		return fmt.Errorf("server URL must start with wss:// or ws://")
	}
	if *insecureWS {
		log.Printf("warning: plain ws:// allowed by -insecure-ws; the agent key crosses the network unencrypted")
		return nil
	}
	host := u.Hostname()
	ips := []net.IP{net.ParseIP(host)}
	if ips[0] == nil {
		if ips, err = net.LookupIP(host); err != nil {
			return fmt.Errorf("can't resolve %s: %v", host, err)
		}
	}
	for _, ip := range ips {
		if !isPrivate(ip) {
			return fmt.Errorf("refusing plain ws:// to %s (a public address); use wss:// (or -insecure-ws)", host)
		}
	}
	return nil
}

func randomPassword(n int) string {
	const chars = "abcdefghjkmnpqrstuvwxyz23456789"
	var sb strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		x, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		sb.WriteByte(chars[x.Int64()])
	}
	return sb.String()
}

func secretFromEnv(v *string, flagName, env string) {
	if *v != "" {
		log.Printf("note: -%s on the command line is visible to other users (ps) and in shell history; prefer env %s", flagName, env)
		return
	}
	*v = os.Getenv(env)
}

func setupIdentity() {
	st, err := loadState()
	if err != nil {
		log.Fatalf("identity: %v", err)
	}
	identity = st.private()
	pubB64 = base64.StdEncoding.EncodeToString(st.public())
	pwSalt = base64.StdEncoding.EncodeToString(st.Salt)
	pwIter = st.Iter

	switch {
	case *password != "":
		if len(*password) < 10 {
			log.Fatal("password must be at least 10 characters")
		}
		pwKey, err = auth.DeriveKey(*password, st.Salt, st.Iter)
	case len(st.Key) == 32 && !*resetPass:
		pwKey = st.Key
	default:
		pw := randomPassword(16)
		if pwKey, err = auth.DeriveKey(pw, st.Salt, st.Iter); err == nil {
			st.Key = pwKey
			err = st.save()
		}
		fmt.Fprintf(os.Stderr, "\nNew password for this computer (shown once, write it down):\n  %s\n\n", pw)
	}
	if err != nil {
		log.Fatalf("password: %v", err)
	}
	fmt.Fprintf(os.Stderr, "Identity fingerprint (the viewer asks you to compare this on first connect):\n  %s\n\n", auth.Fingerprint(st.public()))
}

func main() {
	flag.Parse()
	secretFromEnv(agentKey, "key", "HOPDESK_KEY")
	secretFromEnv(password, "password", "HOPDESK_PASSWORD")
	if *agentKey == "" {
		log.Fatal("the agent key is required: set HOPDESK_KEY (the server prints it when it starts)")
	}
	switch *clipMode {
	case "both", "to-remote", "off":
	default:
		log.Fatal("-clipboard must be both, to-remote or off")
	}
	if err := checkServerURL(*serverURL); err != nil {
		log.Fatal(err)
	}
	if *agentID == "" {
		h, _ := os.Hostname()
		*agentID = strings.ToLower(strings.TrimSuffix(h, ".local"))
	}
	setupIdentity()
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
