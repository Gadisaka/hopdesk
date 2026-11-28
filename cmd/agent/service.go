package main

// Login-screen control (Windows).
//
// Windows keeps the login screen, the lock screen and admin (UAC) prompts on
// a protected desktop that normal programs can't see or control. To reach
// them, HopDesk can install a small Windows service:
//
//   - The service (HopDesk.exe -service) runs as the system account. It
//     starts a worker (HopDesk.exe -worker) in the session shown on the
//     screen, restarts it if it stops, and presses Ctrl+Alt+Del on request.
//   - The worker runs the agent: it connects to the server, shares the
//     screen and replays input, following the protected desktop when it
//     shows (desktop_windows.go).
//   - The desktop app keeps the tray icon and setup page. Settings and the
//     identity move to a folder only administrators and the user who turned
//     this on can open; the worker restarts when they change, and writes its
//     status to the same folder for the app to show.
//
// Nothing here runs unless someone turns it on in the setup page, which asks
// Windows for administrator permission.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const (
	roleApp    = "app"
	roleWorker = "worker"
)

var (
	role        = roleApp
	serviceMode bool // app: the HopDesk service runs the agent; this app shows its status
)

// runSpecialMode handles the service's own command lines. It returns false
// for everything else (the app or the command-line agent).
func runSpecialMode(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "-worker":
		if !serviceSupported() {
			fmt.Fprintln(os.Stderr, "-worker is only used by the HopDesk service on Windows")
			os.Exit(2)
		}
		role = roleWorker
		parent := 0
		if len(args) >= 3 && args[1] == "-parent" {
			parent, _ = strconv.Atoi(args[2])
		}
		runWorker(parent)
		return true
	case "-service", "-service-install", "-service-uninstall":
		os.Exit(runServiceCommand(args))
	}
	return false
}

// detectServiceMode reports whether the HopDesk service runs the agent for
// this user, so the app should only show settings and status.
func detectServiceMode() bool {
	d := serviceDataDir()
	if d == "" {
		return false
	}
	if installed, _ := serviceStatus(); !installed {
		return false
	}
	_, err := os.Stat(filepath.Join(d, "app.json"))
	return err == nil // only readable by administrators and the user who turned it on
}

// ---- the worker ----

type workerStatus struct {
	State    string    `json:"state"`
	Detail   string    `json:"detail"`
	Sessions int       `json:"sessions"`
	Video    string    `json:"video"`
	Sound    string    `json:"sound"`
	Version  string    `json:"version"`
	Build    string    `json:"build"`
	At       time.Time `json:"at"`
}

func workerStatusPath() string { return filepath.Join(appDir(), "status.json") }

func writeWorkerStatus() error {
	var ws workerStatus
	ws.State, ws.Detail = getStatus()
	capMu.Lock()
	ws.Sessions = sessionN - pendingN
	capMu.Unlock()
	ws.Video, ws.Sound = videoDescription(), audioDescription()
	ws.Version, ws.Build, ws.At = version, buildID(), time.Now().UTC()
	data, _ := json.Marshal(ws)
	tmp := workerStatusPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, workerStatusPath())
}

func readWorkerStatus() (workerStatus, error) {
	var ws workerStatus
	data, err := os.ReadFile(workerStatusPath())
	if err != nil {
		return ws, err
	}
	err = json.Unmarshal(data, &ws)
	return ws, err
}

// runWorker is the agent started by the HopDesk service. It uses the shared
// settings, exits when they change (the service starts it again straight
// away), and exits if the service that started it is gone.
func runWorker(parentPID int) {
	setupAppLogging()
	log.Printf("worker started by the HopDesk service (session %d)", currentSessionID())
	if parentPID > 0 {
		go exitWithProcess(parentPID)
	}
	go func() {
		for {
			if err := writeWorkerStatus(); err != nil {
				logOnce("status", "status: %v", err)
			}
			time.Sleep(2 * time.Second)
		}
	}()
	go exitOnChange([]string{appConfigPath(), statePath()}, 2*time.Second)

	c, exists, err := loadAppConfig()
	switch {
	case err != nil:
		setStatus("error", "Settings: "+err.Error())
	case !exists:
		setStatus("error", "The settings for login-screen control are missing. Turn it off and on again.")
	case c.Mode == "host":
		setStatus("error", "Login-screen control works when this computer uses a HopDesk server elsewhere, not when it is the server.")
	default:
		if err := c.validate(); err != nil {
			setStatus("error", "Settings: "+err.Error())
			break
		}
		if err := loadIdentity(true); err != nil {
			setStatus("error", "Identity: "+err.Error())
			break
		}
		if getPendingPassword() != "" {
			log.Printf("no password was set, so a new one was generated: set one in HopDesk's settings")
		}
		startServices(c) // runs until the worker exits
	}
	select {} // keep reporting the error until the settings change
}

// exitOnChange exits when any of the files changes, appears or disappears.
func exitOnChange(paths []string, every time.Duration) {
	snap := func() string {
		s := ""
		for _, p := range paths {
			if fi, err := os.Stat(p); err == nil {
				s += fmt.Sprintf("%s:%d:%d;", p, fi.Size(), fi.ModTime().UnixNano())
			} else {
				s += p + ":-;"
			}
		}
		return s
	}
	first := snap()
	for {
		time.Sleep(every)
		if snap() != first {
			log.Printf("settings changed; restarting the worker")
			os.Exit(0)
		}
	}
}

// ---- the app, while the service runs the agent ----

var worker struct {
	sync.Mutex
	st workerStatus
}

func lastWorkerStatus() workerStatus {
	worker.Lock()
	defer worker.Unlock()
	return worker.st
}

// followWorker mirrors the worker's status in the tray and setup page.
func followWorker() {
	for {
		ws, err := readWorkerStatus()
		_, running := serviceStatus()
		switch {
		case !running:
			ws = workerStatus{}
			setStatus("offline", "The HopDesk service isn't running. Turn login-screen control off and on again, or restart the computer.")
		case err != nil || time.Since(ws.At) > 15*time.Second:
			ws = workerStatus{}
			setStatus("connecting", "Waiting for the HopDesk service to start…")
		default:
			setStatus(ws.State, ws.Detail)
		}
		worker.Lock()
		worker.st = ws
		worker.Unlock()
		time.Sleep(2 * time.Second)
	}
}

// ---- turning it on and off from the setup page ----

var serviceOp struct {
	sync.Mutex
	busy bool
	err  string
}

type serviceInfo struct {
	Supported bool   `json:"supported"`
	On        bool   `json:"on"`
	Running   bool   `json:"running"`
	Stale     bool   `json:"stale"` // the service runs an older copy of HopDesk
	Busy      bool   `json:"busy"`
	Error     string `json:"error,omitempty"`
}

func getServiceInfo() serviceInfo {
	var si serviceInfo
	si.Supported = serviceSupported()
	if !si.Supported {
		return si
	}
	si.On = serviceMode
	_, si.Running = serviceStatus()
	if ws := lastWorkerStatus(); serviceMode && ws.Build != "" && ws.Build != buildID() {
		si.Stale = true
	}
	serviceOp.Lock()
	si.Busy, si.Error = serviceOp.busy, serviceOp.err
	serviceOp.Unlock()
	return si
}

// changeService turns login-screen control on (or refreshes the service's
// copy of HopDesk) or off, then restarts the app in the matching mode.
func changeService(on bool) error {
	serviceOp.Lock()
	if serviceOp.busy {
		serviceOp.Unlock()
		return fmt.Errorf("already working on it")
	}
	serviceOp.busy, serviceOp.err = true, ""
	serviceOp.Unlock()
	go func() {
		err := setServiceEnabled(on)
		serviceOp.Lock()
		serviceOp.busy = false
		if err != nil {
			serviceOp.err = err.Error()
		}
		serviceOp.Unlock()
		if err != nil {
			log.Printf("login-screen control: %v", err)
			return
		}
		log.Printf("login-screen control turned %s; restarting", map[bool]string{true: "on", false: "off"}[on])
		time.Sleep(300 * time.Millisecond)
		restartSelf()
	}()
	return nil
}

// videoDescription is the "Video" line of the setup page.
func videoDescription() string {
	if *codecFlag != "jpeg" && h264Encoder() != "" {
		return "H.264 (" + h264Encoder() + ")"
	}
	return "JPEG frames (install ffmpeg for smoother video)"
}
