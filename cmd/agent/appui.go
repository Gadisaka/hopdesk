package main

// The setup page: a small web UI on 127.0.0.1, opened in the user's browser.
//
// Security: it listens on loopback only; every API call needs a random token
// (kept in a 0600 file, passed to the page in the URL fragment so it never
// appears in requests or logs); Host and Origin are checked to block DNS
// rebinding and other websites; changes need a JSON POST with a custom header,
// which browsers won't send cross-site without a CORS preflight we never allow.

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

//go:embed appui
var appUIFS embed.FS

const uiDefaultPort = 47811

type uiServer struct {
	port  int
	token string
}

type uiLock struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

func uiLockPath() string { return filepath.Join(appDir(), "ui.json") }

func (u *uiServer) url() string { return fmt.Sprintf("http://127.0.0.1:%d/#%s", u.port, u.token) }

func readUILock() uiLock {
	var lk uiLock
	if data, err := os.ReadFile(uiLockPath()); err == nil {
		_ = json.Unmarshal(data, &lk)
	}
	return lk
}

func pingUI(lk uiLock) bool {
	if lk.Port == 0 || lk.Token == "" {
		return false
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/api/ping", lk.Port), nil)
	req.Header.Set("X-HopDesk-Token", lk.Token)
	resp, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// runningVersion asks the HopDesk that is already running which version it is.
func runningVersion(lk uiLock) string {
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/api/state", lk.Port), nil)
	req.Header.Set("X-HopDesk-Token", lk.Token)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var st struct {
		Version string `json:"version"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&st) != nil {
		return ""
	}
	return st.Version
}

// quitRunning asks the HopDesk that is already running to quit, and waits until it has.
func quitRunning(lk uiLock) bool {
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/quit", lk.Port), strings.NewReader("{}"))
	req.Header.Set("X-HopDesk-Token", lk.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		time.Sleep(250 * time.Millisecond)
		if !pingUI(lk) {
			return true
		}
	}
	return false
}

// startUI starts the setup page. If the same version of HopDesk is already
// running, it opens that instance's page and exits. An older or newer version
// that is still running (for example, after installing an update) is asked to
// quit, and this one takes over.
func startUI(restarted bool) (*uiServer, error) {
	lk := readUILock()
	if !restarted && pingUI(lk) {
		other := runningVersion(lk)
		if other != version && quitRunning(lk) {
			log.Printf("replaced HopDesk %q that was still running", other)
		} else {
			log.Printf("HopDesk %s is already running; opening its page", other)
			openURL(fmt.Sprintf("http://127.0.0.1:%d/#%s", lk.Port, lk.Token))
			os.Exit(0)
		}
	}
	if len(lk.Token) != 64 {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		lk.Token = hex.EncodeToString(b)
	}
	var ln net.Listener
	var err error
	deadline := time.Now().Add(6 * time.Second) // after a restart the old process may still hold the port
	for {
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", uiDefaultPort))
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
				return nil, err
			}
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	u := &uiServer{port: ln.Addr().(*net.TCPAddr).Port, token: lk.Token}
	lk.Port = u.port
	_ = os.MkdirAll(appDir(), 0o700)
	data, _ := json.Marshal(lk)
	if err := os.WriteFile(uiLockPath(), data, 0o600); err != nil {
		log.Printf("setup page lock file: %v", err)
	}
	srv := &http.Server{Handler: u.handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { log.Printf("setup page: %v", srv.Serve(ln)) }()
	log.Printf("setup page on http://127.0.0.1:%d", u.port)
	return u, nil
}

func (u *uiServer) hostOK(host string) bool {
	return host == fmt.Sprintf("127.0.0.1:%d", u.port) || host == fmt.Sprintf("localhost:%d", u.port)
}

func (u *uiServer) handler() http.Handler {
	sub, _ := fs.Sub(appUIFS, "appui")
	static := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !u.hostOK(r.Host) { // DNS rebinding: someone else's domain pointed at 127.0.0.1
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			u.api(w, r)
			return
		}
		static.ServeHTTP(w, r)
	})
}

func (u *uiServer) authorized(r *http.Request) bool {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-HopDesk-Token")), []byte(u.token)) != 1 {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && !u.hostOK(strings.TrimPrefix(o, "http://")) {
		return false
	}
	if r.Method != http.MethodGet && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (u *uiServer) api(w http.ResponseWriter, r *http.Request) {
	if !u.authorized(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not allowed"})
		return
	}
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case "/api/ping":
			writeJSON(w, 200, map[string]bool{"ok": true})
		case "/api/state":
			writeJSON(w, 200, buildState())
		default:
			http.NotFound(w, r)
		}
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	switch r.URL.Path {
	case "/api/config":
		var c appConfig
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad request"})
			return
		}
		c.normalize()
		if err := c.validate(); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err := c.save(); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		warn := ""
		if err := setAutostart(c.StartAtLogin); err != nil {
			warn = "Saved, but start-at-login couldn't be changed: " + err.Error()
			log.Printf("autostart: %v", err)
		}
		log.Printf("settings saved; restarting")
		writeJSON(w, 200, map[string]any{"ok": true, "warning": warn})
		go func() { time.Sleep(500 * time.Millisecond); restartSelf() }()

	case "/api/password":
		var in struct {
			Action   string `json:"action"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad request"})
			return
		}
		switch in.Action {
		case "generate":
			pw := randomPassword(16)
			if err := setPassword(pw); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
			setPendingPassword(pw)
			log.Printf("new password generated")
			writeJSON(w, 200, map[string]string{"password": pw})
		case "set":
			if len(in.Password) < 10 {
				writeJSON(w, 400, map[string]string{"error": "use at least 10 characters"})
				return
			}
			if err := setPassword(in.Password); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
			setPendingPassword("")
			log.Printf("password changed")
			writeJSON(w, 200, map[string]bool{"ok": true})
		case "ack":
			setPendingPassword("")
			writeJSON(w, 200, map[string]bool{"ok": true})
		default:
			writeJSON(w, 400, map[string]string{"error": "unknown action"})
		}

	case "/api/permission":
		var in struct {
			Which string `json:"which"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Which != "screen" && in.Which != "accessibility" {
			writeJSON(w, 400, map[string]string{"error": "unknown permission"})
			return
		}
		requestPermission(in.Which)
		writeJSON(w, 200, map[string]bool{"ok": true})

	case "/api/tailscale-logout":
		if err := tailnetLogout(); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		go func() { time.Sleep(500 * time.Millisecond); restartSelf() }()

	case "/api/new-key":
		writeJSON(w, 200, map[string]string{"agentKey": newAgentKey()})

	case "/api/open-log":
		openURL(logPath())
		writeJSON(w, 200, map[string]bool{"ok": true})

	case "/api/restart":
		writeJSON(w, 200, map[string]bool{"ok": true})
		go func() { time.Sleep(300 * time.Millisecond); restartSelf() }()

	case "/api/quit":
		writeJSON(w, 200, map[string]bool{"ok": true})
		go func() { time.Sleep(300 * time.Millisecond); quitApp() }()

	default:
		http.NotFound(w, r)
	}
}

func newAgentKey() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type uiState struct {
	Version     string    `json:"version"`
	OS          string    `json:"os"`
	State       string    `json:"state"`
	Detail      string    `json:"detail"`
	Sessions    int       `json:"sessions"`
	Fingerprint string    `json:"fingerprint"`
	PasswordSet bool      `json:"passwordSet"`
	NewPassword string    `json:"newPassword,omitempty"`
	SetupDone   bool      `json:"setupDone"`
	Config      appConfig `json:"config"`
	Video       string    `json:"video"`
	Perms       struct {
		Supported     bool `json:"supported"`
		Screen        bool `json:"screen"`
		Accessibility bool `json:"accessibility"`
	} `json:"perms"`
	Autostart bool `json:"autostartSupported"`
	Host      struct {
		LocalURL  string       `json:"localUrl"`
		ViewerURL string       `json:"viewerUrl"`
		AgentURL  string       `json:"agentUrl"`
		Tailnet   tailnetState `json:"tailnet"`
	} `json:"host"`
}

func buildState() uiState {
	var st uiState
	st.Version, st.OS = version, runtime.GOOS
	st.State, st.Detail = getStatus()
	st.Sessions = activeSessions()
	st.Fingerprint = identityFingerprint()
	st.PasswordSet = getPwKey() != nil
	st.NewPassword = getPendingPassword()
	appMu.Lock()
	st.Config, st.SetupDone = appCfg, appCfgExists
	appMu.Unlock()
	st.Video = "JPEG frames (install ffmpeg for smoother video)"
	if *codecFlag != "jpeg" && h264Encoder() != "" {
		st.Video = "H.264 (" + h264Encoder() + ")"
	}
	st.Perms.Supported = permSupported()
	if st.Perms.Supported {
		st.Perms.Screen, st.Perms.Accessibility = permScreen(), permAccessibility()
	}
	st.Autostart = autostartSupported()
	st.Host.LocalURL = "http://localhost:" + st.Config.hostPort()
	st.Host.Tailnet = getTailnet()
	if t := st.Host.Tailnet; t.Public && t.DNSName != "" {
		st.Host.ViewerURL = "https://" + t.DNSName
		st.Host.AgentURL = "wss://" + t.DNSName + "/ws"
	}
	return st
}

var errUnsupported = errors.New("not supported on this system")
