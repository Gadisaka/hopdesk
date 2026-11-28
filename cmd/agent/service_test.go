package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkerStatusRoundTrip(t *testing.T) {
	old := *stateFile
	*stateFile = filepath.Join(t.TempDir(), "agent.json")
	defer func() { *stateFile = old }()

	setStatus("online", "")
	if err := writeWorkerStatus(); err != nil {
		t.Fatal(err)
	}
	ws, err := readWorkerStatus()
	if err != nil {
		t.Fatal(err)
	}
	if ws.State != "online" || ws.Build != buildID() || ws.Version != version || time.Since(ws.At) > time.Minute {
		t.Fatalf("got %+v", ws)
	}
}

func TestHostModeBlockedWhileServiceRuns(t *testing.T) {
	c := defaultAppConfig()
	c.Mode, c.AgentKey = "host", strings.Repeat("k", 32)
	if err := c.validate(); err != nil {
		t.Fatalf("host mode should be fine normally: %v", err)
	}
	serviceMode = true
	defer func() { serviceMode = false }()
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "login-screen control") {
		t.Fatalf("want a login-screen control error, got %v", err)
	}
}

func TestStaleServiceDetected(t *testing.T) {
	serviceMode = true
	defer func() { serviceMode = false; worker.st = workerStatus{} }()
	worker.st = workerStatus{Build: "something-else"}
	// Only Windows has the service; elsewhere it's reported as unsupported.
	if si := getServiceInfo(); si.Supported != serviceSupported() || (si.Supported && !si.Stale) {
		t.Fatalf("got %+v", si)
	}
}

func TestPerUserFilesStayInUserFolder(t *testing.T) {
	serviceMode = true
	defer func() { serviceMode = false }()
	for _, p := range []string{uiLockPath(), updatePrefsPath()} {
		if filepath.Dir(p) != userDir() {
			t.Errorf("%s should be in %s", p, userDir())
		}
	}
}

func TestSpecialModesLeaveOtherArgsAlone(t *testing.T) {
	for _, args := range [][]string{nil, {"-app"}, {"-server", "wss://x/ws"}} {
		if runSpecialMode(args) {
			t.Errorf("%v was taken as a service command", args)
		}
	}
}
