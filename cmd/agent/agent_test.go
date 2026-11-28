package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGuardLocksOneAddressNotOthers(t *testing.T) {
	g := &guardT{ips: map[string]*ipFail{}}
	for i := 0; i < perIPFails; i++ {
		if !g.check("1.2.3.4") {
			t.Fatalf("attempt %d refused too early", i+1)
		}
		g.fail("1.2.3.4")
	}
	if g.check("1.2.3.4") {
		t.Fatal("attacker address not locked after repeated wrong passwords")
	}
	if !g.check("5.6.7.8") {
		t.Fatal("a different address (you) was locked out too")
	}
	if until := g.ips["1.2.3.4"].until; time.Until(until) < baseLockout-time.Minute {
		t.Fatalf("lockout too short: %s", time.Until(until))
	}
}

func TestGuardLockoutGrows(t *testing.T) {
	g := &guardT{ips: map[string]*ipFail{}}
	for i := 0; i < 2*perIPFails; i++ {
		g.fail("1.2.3.4")
	}
	if d := time.Until(g.ips["1.2.3.4"].until); d < 2*baseLockout-time.Minute {
		t.Fatalf("second lockout should be longer, got %s", d)
	}
}

func TestGuardCapsDistributedGuessing(t *testing.T) {
	g := &guardT{ips: map[string]*ipFail{}}
	for i := 0; i < globalFails; i++ {
		g.fail("10.0.0." + string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	if g.check("9.9.9.9") {
		t.Fatal("rotating addresses got past the hourly cap")
	}
}

func TestSessionCaps(t *testing.T) {
	pendingN, sessionN = 0, 0
	for i := 0; i < maxPending; i++ {
		if !reserveSession() {
			t.Fatal("refused below the cap")
		}
	}
	if reserveSession() {
		t.Fatal("more unauthenticated sessions than allowed")
	}
	promoteSession() // one logs in
	if !reserveSession() {
		t.Fatal("a slot should be free after one session authenticated")
	}
	if reserveSession() {
		t.Fatal("total session cap not enforced")
	}
	pendingN, sessionN = 0, 0
}

func TestServerURLPolicy(t *testing.T) {
	*insecureWS = false
	for _, u := range []string{"wss://example.com/ws", "ws://127.0.0.1:8080/ws", "ws://192.168.1.10:8080/ws", "ws://100.101.102.103:8080/ws", "ws://[::1]:8080/ws"} {
		if err := checkServerURL(u); err != nil {
			t.Errorf("%s refused: %v", u, err)
		}
	}
	for _, u := range []string{"ws://8.8.8.8:8080/ws", "http://127.0.0.1/ws", "ws://203.0.113.5/ws"} {
		if err := checkServerURL(u); err == nil {
			t.Errorf("%s allowed", u)
		}
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":    "passwd",
		`..\..\Windows\x.dll`: "x.dll",
		"CON":                 "_CON",
		"nul.txt":             "_nul.txt",
		"report.pdf. . ":      "report.pdf",
		"a:b?.txt":            "a_b_.txt",
		"..":                  "",
		"":                    "",
	}
	for in, want := range cases {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIdentityFileIsPrivateAndStable(t *testing.T) {
	*stateFile = filepath.Join(t.TempDir(), "hopdesk", "agent.json")
	defer func() { *stateFile = "" }()
	a, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	b, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Seed) != string(b.Seed) {
		t.Fatal("identity changed between runs")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(*stateFile)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("identity file mode %v, want 0600", fi.Mode().Perm())
		}
		os.Chmod(*stateFile, 0o644)
		if _, err := loadState(); err == nil {
			t.Fatal("a world-readable identity file should be refused")
		}
	}
}

func TestRandomPassword(t *testing.T) {
	p := randomPassword(16)
	if !regexp.MustCompile(`^[a-z2-9]{4}(-[a-z2-9]{4}){3}$`).MatchString(p) {
		t.Fatalf("unexpected password format %q", p)
	}
	if p == randomPassword(16) {
		t.Fatal("passwords repeat")
	}
}

func TestHardwareEncodersGetLowLatencyOptions(t *testing.T) {
	for _, c := range []struct{ codec, want string }{
		{"h264_nvenc", "-zerolatency 1"},
		{"h264_qsv", "-async_depth 1"},
		{"h264_amf", "-usage ultralowlatency"},
	} {
		if got := strings.Join(encoderOpts(c.codec, false, "high"), " "); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q missing from %q", c.codec, c.want, got)
		}
	}
	if got := strings.Join(encoderOpts("h264_mf", true, "high"), " "); !strings.Contains(got, "-hw_encoding 1") {
		t.Errorf("h264_mf on the graphics chip: got %q", got)
	}
	if got := strings.Join(encoderOpts("h264_amf", false, "baseline"), " "); !strings.Contains(got, "constrained_baseline") {
		t.Errorf("AMF baseline: got %q", got)
	}
	if encoderPixFmt("h264_qsv") != "nv12" || encoderPixFmt("libx264") != "yuv420p" {
		t.Error("wrong pixel formats")
	}
}
