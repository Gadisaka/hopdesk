package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, err := New(ctx, Config{AgentKey: strings.Repeat("k", 24)})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	s.mu.Lock()
	s.agents["studio-mac"] = &peer{}
	s.mu.Unlock()
	return s, ts
}

func getOnline(t *testing.T, ts *httptest.Server, ids string, hdr map[string]string) (int, map[string]bool) {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+"/online?ids="+ids, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Online map[string]bool `json:"online"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body.Online
}

func TestOnlineReportsOnlyAskedDevices(t *testing.T) {
	_, ts := newTestServer(t)
	code, on := getOnline(t, ts, "studio-mac,office-pc", map[string]string{"Sec-Fetch-Site": "same-origin"})
	if code != 200 || !on["studio-mac"] || on["office-pc"] || len(on) != 2 {
		t.Fatalf("code %d, online %v", code, on)
	}
}

func TestOnlineRefusesOtherSites(t *testing.T) {
	_, ts := newTestServer(t)
	if code, _ := getOnline(t, ts, "studio-mac", map[string]string{"Sec-Fetch-Site": "cross-site"}); code != http.StatusForbidden {
		t.Fatalf("cross-site request answered with %d", code)
	}
	if code, _ := getOnline(t, ts, "studio-mac", map[string]string{"Origin": "https://evil.example"}); code != http.StatusForbidden {
		t.Fatalf("foreign origin answered with %d", code)
	}
}

func TestOnlineIsRateLimitedAndCapped(t *testing.T) {
	_, ts := newTestServer(t)
	many := strings.Repeat("dev,", 40) + "studio-mac"
	code, on := getOnline(t, ts, many, nil)
	if code != 200 || len(on) > presenceMaxIDs {
		t.Fatalf("code %d, %d names answered", code, len(on))
	}
	limited := false
	for i := 0; i < presenceBurst+2; i++ {
		if code, _ := getOnline(t, ts, "studio-mac", nil); code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("presence checks were never rate limited")
	}
}

func TestPageAllowsTheAppManifest(t *testing.T) {
	_, ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "manifest-src 'self'") {
		t.Fatalf("manifest: %d, CSP %q", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}
}
