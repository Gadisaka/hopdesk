package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func testUI(t *testing.T) (*uiServer, http.Handler) {
	*stateFile = filepath.Join(t.TempDir(), "agent.json")
	t.Cleanup(func() { *stateFile = "" })
	u := &uiServer{port: 47811, token: strings.Repeat("ab", 32)}
	appMu.Lock()
	appCfg = defaultAppConfig()
	appMu.Unlock()
	return u, u.handler()
}

func do(h http.Handler, method, path, host string, hdr map[string]string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = host
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSetupPageSecurity(t *testing.T) {
	u, h := testUI(t)
	host := "127.0.0.1:47811"
	tok := map[string]string{"X-HopDesk-Token": u.token}

	if w := do(h, "GET", "/", host, nil, ""); w.Code != 200 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("page: %d, CSP %q", w.Code, w.Header().Get("Content-Security-Policy"))
	}
	if w := do(h, "GET", "/api/ping", host, tok, ""); w.Code != 200 {
		t.Fatalf("ping with token: %d", w.Code)
	}
	if w := do(h, "GET", "/api/state", host, nil, ""); w.Code != 403 {
		t.Fatalf("state without token: %d", w.Code)
	}
	if w := do(h, "GET", "/api/state", host, map[string]string{"X-HopDesk-Token": "wrong"}, ""); w.Code != 403 {
		t.Fatalf("state with wrong token: %d", w.Code)
	}
	// DNS rebinding: evil.example resolving to 127.0.0.1
	if w := do(h, "GET", "/api/ping", "evil.example:47811", tok, ""); w.Code != 403 {
		t.Fatalf("foreign Host header accepted: %d", w.Code)
	}
	// another website making the request
	evil := map[string]string{"X-HopDesk-Token": u.token, "Origin": "https://evil.example", "Content-Type": "application/json"}
	if w := do(h, "POST", "/api/password", host, evil, `{"action":"generate"}`); w.Code != 403 {
		t.Fatalf("cross-origin POST accepted: %d", w.Code)
	}
	// form-style POST (what a cross-site <form> could send)
	form := map[string]string{"X-HopDesk-Token": u.token, "Content-Type": "application/x-www-form-urlencoded"}
	if w := do(h, "POST", "/api/quit", host, form, "x=1"); w.Code != 403 {
		t.Fatalf("non-JSON POST accepted: %d", w.Code)
	}
}

func TestSetupPageRejectsBadSettings(t *testing.T) {
	u, h := testUI(t)
	hdr := map[string]string{"X-HopDesk-Token": u.token, "Content-Type": "application/json", "Origin": "http://127.0.0.1:47811"}
	cases := map[string]string{
		"bad device name": `{"deviceId":"my mac!","mode":"connect","serverUrl":"wss://x.ts.net/ws","agentKey":"0123456789abcdef0","clipboard":"both","quality":"high","fps":30}`,
		"plain ws public": `{"deviceId":"mac","mode":"connect","serverUrl":"ws://8.8.8.8/ws","agentKey":"0123456789abcdef0","clipboard":"both","quality":"high","fps":30}`,
		"short agent key": `{"deviceId":"mac","mode":"connect","serverUrl":"wss://x.ts.net/ws","agentKey":"short","clipboard":"both","quality":"high","fps":30}`,
		"bad fps":         `{"deviceId":"mac","mode":"connect","serverUrl":"wss://x.ts.net/ws","agentKey":"0123456789abcdef0","clipboard":"both","quality":"high","fps":999}`,
		"bad listen addr": `{"deviceId":"mac","mode":"host","hostListen":"nonsense","clipboard":"both","quality":"high","fps":30}`,
		"unknown mode":    `{"deviceId":"mac","mode":"weird","agentKey":"0123456789abcdef0","clipboard":"both","quality":"high","fps":30}`,
	}
	for name, body := range cases {
		if w := do(h, "POST", "/api/config", "127.0.0.1:47811", hdr, body); w.Code != 400 {
			t.Errorf("%s: got %d, want 400 (%s)", name, w.Code, w.Body.String())
		}
	}
}

func TestHostModeGetsAKey(t *testing.T) {
	c := defaultAppConfig()
	c.Mode = "host"
	c.normalize()
	if len(c.AgentKey) < 32 {
		t.Fatalf("host mode should generate an agent key, got %q", c.AgentKey)
	}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if c.localWS() != "ws://127.0.0.1:8080/ws" {
		t.Fatal(c.localWS())
	}
}

func TestPasswordChangeTakesEffectImmediately(t *testing.T) {
	*stateFile = filepath.Join(t.TempDir(), "agent.json")
	defer func() { *stateFile = "" }()
	if err := loadIdentity(true); err != nil {
		t.Fatal(err)
	}
	if getPendingPassword() == "" {
		t.Fatal("a generated password should be waiting to be shown")
	}
	before := string(getPwKey())
	if err := setPassword("another-long-password"); err != nil {
		t.Fatal(err)
	}
	if string(getPwKey()) == before {
		t.Fatal("password key didn't change")
	}
}
