package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"hopdesk/internal/update"
)

// fakeReleases serves a signed release like GitHub does.
func fakeReleases(t *testing.T, ver string, installer []byte, corrupt bool) (pubB64 string) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sum := sha256.Sum256(installer)
	name := "HopDesk-" + ver + "-test.bin"
	m := update.Manifest{Version: ver, NotesURL: "https://example.com/notes", Assets: map[string]update.Asset{
		runtime.GOOS: {Name: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(installer))},
	}}
	data, _ := json.Marshal(m)
	sig := update.Sign(priv, data)
	served := installer
	if corrupt {
		served = append([]byte{}, installer...)
		served[0] ^= 0xff
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest/download/latest.json":
			w.Write(data)
		case "/latest/download/latest.json.sig":
			w.Write([]byte(sig))
		case "/download/" + ver + "/" + name:
			w.Write(served)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	oldURL, oldKey, oldVer, oldState := updateBaseURL, updateKey, version, *stateFile
	updateBaseURL = srv.URL
	version = "v0.4.0"
	*stateFile = filepath.Join(t.TempDir(), "agent.json")
	t.Cleanup(func() {
		updateBaseURL, updateKey, version, *stateFile = oldURL, oldKey, oldVer, oldState
		upd.Lock()
		upd.s, upd.m, upd.prefs = updateState{}, nil, updatePrefs{}
		upd.Unlock()
	})
	return base64.StdEncoding.EncodeToString(pub)
}

func needsUpdatablePlatform(t *testing.T) {
	updateAnyOS = true
	t.Cleanup(func() { updateAnyOS = false })
}

func TestUpdateFoundAndDownloaded(t *testing.T) {
	needsUpdatablePlatform(t)
	installer := []byte(strings.Repeat("new HopDesk ", 5000))
	updateKey = fakeReleases(t, "v0.5.0", installer, false)

	if err := checkForUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := getUpdateState(); s.State != "available" || s.Latest != "v0.5.0" {
		t.Fatalf("state = %+v", s)
	}
	upd.Lock()
	m := upd.m
	upd.Unlock()
	path, err := downloadAsset(m)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(path))
	got, _ := os.ReadFile(path)
	if string(got) != string(installer) {
		t.Fatal("downloaded file differs")
	}
}

func TestCorruptDownloadIsRejected(t *testing.T) {
	needsUpdatablePlatform(t)
	updateKey = fakeReleases(t, "v0.5.0", []byte(strings.Repeat("x", 4096)), true)
	if err := checkForUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	upd.Lock()
	m := upd.m
	upd.Unlock()
	if _, err := downloadAsset(m); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("corrupt download accepted: %v", err)
	}
}

func TestOlderOrForgedReleaseIsIgnored(t *testing.T) {
	needsUpdatablePlatform(t)
	updateKey = fakeReleases(t, "v0.3.0", []byte("old"), false)
	_ = checkForUpdate(context.Background())
	if s := getUpdateState(); s.State != "current" {
		t.Fatalf("older release offered: %+v", s)
	}

	// Same server, but HopDesk trusts a different key: nothing is offered.
	fakeReleases(t, "v0.9.0", []byte("forged"), false)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	updateKey = base64.StdEncoding.EncodeToString(other)
	if err := checkForUpdate(context.Background()); err == nil {
		t.Fatal("forged release accepted")
	}
	if s := getUpdateState(); s.State == "available" {
		t.Fatalf("forged release offered: %+v", s)
	}
}

func TestUpdatesOffWithoutKey(t *testing.T) {
	old := updateKey
	updateKey = ""
	defer func() { updateKey = old }()
	if s := getUpdateState(); s.State != "off" || s.Message == "" {
		t.Fatalf("state = %+v", s)
	}
}
