package main

// Desktop app mode: started by double-clicking HopDesk (no command-line
// arguments). Settings live in a config file and are edited in a local,
// token-protected setup page; a tray / menu-bar icon shows the status.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"hopdesk/internal/server"
)

var version = "dev" // set at build time: -ldflags "-X main.version=v1.2.3"

type appConfig struct {
	DeviceID     string `json:"deviceId"`
	Mode         string `json:"mode"`       // "connect": use a server elsewhere; "host": this computer is the server
	ServerURL    string `json:"serverUrl"`  // connect mode
	AgentKey     string `json:"agentKey"`   // the server's agent key (host mode: generated here)
	HostListen   string `json:"hostListen"` // host mode listen address
	Confirm      bool   `json:"confirm"`
	Clipboard    string `json:"clipboard"`
	Quality      string `json:"quality"`
	FPS          int    `json:"fps"`
	StartAtLogin bool   `json:"startAtLogin"`
}

var (
	appMu        sync.Mutex
	appCfg       appConfig
	appCfgExists bool
)

func appDir() string {
	if *stateFile != "" {
		return filepath.Dir(*stateFile)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "hopdesk")
}

func appConfigPath() string { return filepath.Join(appDir(), "app.json") }
func logPath() string       { return filepath.Join(appDir(), "hopdesk.log") }

func defaultAppConfig() appConfig {
	h, _ := os.Hostname()
	id := strings.ToLower(strings.TrimSuffix(h, ".local"))
	id = regexp.MustCompile(`[^a-z0-9._-]+`).ReplaceAllString(id, "-")
	if id == "" {
		id = "my-computer"
	}
	return appConfig{DeviceID: id, Mode: "connect", HostListen: "127.0.0.1:8080",
		Clipboard: "both", Quality: "high", FPS: 30, StartAtLogin: true}
}

func loadAppConfig() (appConfig, bool, error) {
	c := defaultAppConfig()
	data, err := os.ReadFile(appConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return defaultAppConfig(), false, fmt.Errorf("settings file is damaged: %v", err)
	}
	return c, true, nil
}

func (c appConfig) save() error {
	if err := os.MkdirAll(appDir(), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	tmp := appConfigPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, appConfigPath())
}

var deviceIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func (c *appConfig) normalize() {
	c.DeviceID = strings.TrimSpace(c.DeviceID)
	c.ServerURL = strings.TrimSpace(c.ServerURL)
	c.AgentKey = strings.TrimSpace(c.AgentKey)
	c.HostListen = strings.TrimSpace(c.HostListen)
	if c.HostListen == "" {
		c.HostListen = "127.0.0.1:8080"
	}
	if c.Mode == "host" && c.AgentKey == "" {
		c.AgentKey = server.RandomKey()
	}
}

func (c appConfig) validate() error {
	if !deviceIDRe.MatchString(c.DeviceID) {
		return errors.New("device name: use 1–64 letters, numbers, dots, dashes or underscores")
	}
	switch c.Mode {
	case "connect":
		if c.ServerURL == "" {
			return errors.New("enter the server address (e.g. wss://mymac.tailnet.ts.net/ws)")
		}
		if err := checkServerURL(c.ServerURL); err != nil {
			return err
		}
	case "host":
		if _, _, err := net.SplitHostPort(c.HostListen); err != nil {
			return errors.New("listen address must look like 127.0.0.1:8080")
		}
	default:
		return errors.New("choose how this computer connects")
	}
	if len(c.AgentKey) < 16 {
		return errors.New("the agent key must be at least 16 characters (copy it from the server)")
	}
	if c.Clipboard != "both" && c.Clipboard != "to-remote" && c.Clipboard != "off" {
		return errors.New("invalid clipboard setting")
	}
	if _, ok := presets[c.Quality]; !ok {
		return errors.New("invalid quality")
	}
	if !allowedFPS[c.FPS] {
		return errors.New("invalid frame rate")
	}
	return nil
}

// localWS is the address this computer's own agent uses to reach its built-in server.
func (c appConfig) localWS() string {
	_, port, _ := net.SplitHostPort(c.HostListen)
	return "ws://127.0.0.1:" + port + "/ws"
}

func (c appConfig) hostPort() string {
	_, port, _ := net.SplitHostPort(c.HostListen)
	return port
}

// ---- status shown in the tray and setup page ----

var status struct {
	sync.Mutex
	state  string // setup, connecting, online, offline, error
	detail string
}

func setStatus(state, detail string) {
	status.Lock()
	status.state, status.detail = state, detail
	status.Unlock()
}

func getStatus() (string, string) {
	status.Lock()
	defer status.Unlock()
	if status.state == "" {
		return "starting", ""
	}
	return status.state, status.detail
}

func activeSessions() int {
	capMu.Lock()
	defer capMu.Unlock()
	return sessionN - pendingN
}

// ---- app startup ----

func setupAppLogging() {
	_ = os.MkdirAll(appDir(), 0o700)
	if fi, err := os.Stat(logPath()); err == nil && fi.Size() > 5<<20 {
		_ = os.Rename(logPath(), logPath()+".old")
	}
	f, err := os.OpenFile(logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(f, stderrOrDiscard()))
	log.Printf("HopDesk %s starting", version)
}

// stderrOrDiscard avoids failing writes when there's no console (Windows GUI build).
func stderrOrDiscard() io.Writer {
	if _, err := os.Stderr.Stat(); err != nil {
		return io.Discard
	}
	return os.Stderr
}

func runApp(restarted bool) {
	setupAppLogging()
	c, exists, err := loadAppConfig()
	if err != nil {
		log.Printf("settings: %v", err)
	}
	appMu.Lock()
	appCfg, appCfgExists = c, exists
	appMu.Unlock()

	if err := loadIdentity(true); err != nil {
		log.Printf("identity: %v", err)
		setStatus("error", err.Error())
	}
	ui, err := startUI(restarted)
	if err != nil {
		log.Fatalf("setup page: %v", err)
	}
	startUpdateLoop()
	if exists && c.validate() == nil {
		go startServices(c)
	} else {
		setStatus("setup", "Almost done: choose how this computer connects under Connection, then click Save and restart.")
		openURL(ui.url())
	}
	runTray(ui) // blocks until Quit
}

// startServices runs the built-in server (host mode) and the agent.
func startServices(c appConfig) {
	*agentID, *agentKey = c.DeviceID, c.AgentKey
	*confirmFlag, *clipMode = c.Confirm, c.Clipboard
	*defQuality, *defFPS = c.Quality, c.FPS
	*serverURL = c.ServerURL

	if c.Mode == "host" {
		// The built-in server: on this computer for its own agent and browser,
		// and published through the bundled Tailscale for everyone else.
		*serverURL = c.localWS()
		srv, err := server.New(context.Background(), server.Config{Addr: c.HostListen, AgentKey: c.AgentKey, STUN: server.DefaultSTUN})
		if err != nil {
			setStatus("error", "Built-in server: "+err.Error())
			return
		}
		ln, err := net.Listen("tcp", c.HostListen)
		if err != nil {
			setStatus("error", "Built-in server: "+err.Error())
			return
		}
		go func() {
			if err := srv.Serve(ln); err != nil {
				log.Printf("built-in server: %v", err)
				setStatus("error", "Built-in server: "+err.Error())
			}
		}()
		go startTailnet(srv)
	}
	if err := checkServerURL(*serverURL); err != nil {
		setStatus("error", err.Error())
		return
	}
	if err := platformInit(); err != nil {
		log.Printf("warning: %v", err)
	}
	if err := initCapture(); err != nil {
		setStatus("error", "Screen capture: "+err.Error())
		return
	}
	if *codecFlag != "jpeg" {
		h264Encoder()
	}
	agentLoop()
}

// agentLoop keeps the agent connected to the server, with backoff.
func agentLoop() {
	backoff := 2 * time.Second
	for {
		start := time.Now()
		setStatus("connecting", "")
		err := runOnce()
		log.Printf("disconnected from server: %v", err)
		setStatus("offline", friendlyError(err))
		if time.Since(start) > time.Minute {
			backoff = 2 * time.Second
		}
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func friendlyError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "bad agent key"):
		return "The server rejected the agent key. Copy it again from the server."
	case strings.Contains(s, "id already online"):
		return "Another computer is already online with this device name."
	case strings.Contains(s, "too many wrong agent keys"):
		return "Locked out by the server after wrong agent keys. Wait 15 minutes."
	case strings.Contains(s, "connection refused"), strings.Contains(s, "no such host"), strings.Contains(s, "i/o timeout"):
		return "Can't reach the server. Check the address and that the server is running."
	}
	return s
}

// ---- password shown once in the setup page ----

var pending struct {
	sync.Mutex
	password string
}

func setPendingPassword(p string) { pending.Lock(); pending.password = p; pending.Unlock() }
func getPendingPassword() string  { pending.Lock(); defer pending.Unlock(); return pending.password }
