package main

// Automatic updates. HopDesk checks the latest GitHub release once a day (or
// when asked), verifies the signed manifest (see internal/update), and on
// request downloads the installer for this system, checks its SHA-256, and
// hands it to the platform installer (update_darwin.go, update_windows.go).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"hopdesk/internal/update"
)

const updateRepo = "gadisaka/hopdesk"

var (
	updateBaseURL = "https://github.com/" + updateRepo + "/releases" // overridden in tests
	updateKey     = updatePublicKey                                  // overridden in tests
	updateAnyOS   = false                                            // tests: allow checks on any system
)

// updateState is what the settings page shows.
type updateState struct {
	State    string    `json:"state"` // off, idle, checking, current, available, downloading, installing, error
	Current  string    `json:"current"`
	Latest   string    `json:"latest,omitempty"`
	NotesURL string    `json:"notesUrl,omitempty"`
	Progress int       `json:"progress,omitempty"` // percent, while downloading
	Message  string    `json:"message,omitempty"`  // why updates are off, or what went wrong
	Checked  time.Time `json:"checked,omitempty"`
	Auto     bool      `json:"auto"`
}

// updatePrefs is kept apart from app.json so changing it never needs a restart.
type updatePrefs struct {
	NoAutoCheck bool   `json:"noAutoCheck,omitempty"`
	Notified    string `json:"notified,omitempty"` // last version we sent a notification about
}

var upd struct {
	sync.Mutex
	s     updateState
	m     *update.Manifest
	prefs updatePrefs
}

func updatePrefsPath() string { return filepath.Join(appDir(), "update.json") }

func loadUpdatePrefs() {
	var p updatePrefs
	if data, err := os.ReadFile(updatePrefsPath()); err == nil {
		_ = json.Unmarshal(data, &p)
	}
	upd.Lock()
	upd.prefs = p
	upd.Unlock()
}

// saveUpdatePrefs must be called with upd locked.
func saveUpdatePrefs() {
	data, _ := json.Marshal(upd.prefs)
	_ = os.MkdirAll(appDir(), 0o700)
	if err := os.WriteFile(updatePrefsPath(), data, 0o600); err != nil {
		log.Printf("update: saving preferences: %v", err)
	}
}

// updatesSupported says whether this copy can update itself, and if not, why.
func updatesSupported() (bool, string) {
	if updateKey == "" {
		return false, "Automatic updates aren't set up for this build."
	}
	if _, ok := update.ParseVersion(version); !ok {
		return false, "This is a development build, so it doesn't update itself."
	}
	switch {
	case updateAnyOS:
	case runtime.GOOS == "darwin":
		if appBundle() == "" {
			return false, "Run HopDesk from the Applications folder to get automatic updates."
		}
	case runtime.GOOS == "windows":
	default:
		return false, "Automatic updates are available on macOS and Windows."
	}
	return true, ""
}

func getUpdateState() updateState {
	upd.Lock()
	defer upd.Unlock()
	s := upd.s
	s.Current = version
	s.Auto = !upd.prefs.NoAutoCheck
	if ok, why := updatesSupported(); !ok {
		s.State, s.Message = "off", why
	} else if s.State == "" {
		s.State = "idle"
	}
	return s
}

func setUpdate(f func(s *updateState)) {
	upd.Lock()
	f(&upd.s)
	upd.Unlock()
}

func setAutoUpdateCheck(on bool) {
	upd.Lock()
	upd.prefs.NoAutoCheck = !on
	saveUpdatePrefs()
	upd.Unlock()
}

func busyUpdating() bool {
	upd.Lock()
	defer upd.Unlock()
	return upd.s.State == "checking" || upd.s.State == "downloading" || upd.s.State == "installing"
}

var updateHTTP = &http.Client{Timeout: 30 * time.Second}

func fetchSmall(ctx context.Context, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "HopDesk/"+version)
	resp, err := updateHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNoRelease
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errors.New("the update description is too large")
	}
	return data, nil
}

var errNoRelease = errors.New("no release with automatic updates has been published yet")

// checkForUpdate fetches and verifies the latest manifest.
func checkForUpdate(ctx context.Context) error {
	if ok, _ := updatesSupported(); !ok {
		return nil
	}
	if busyUpdating() {
		return nil
	}
	setUpdate(func(s *updateState) { s.State, s.Message, s.Progress = "checking", "", 0 })
	m, err := fetchManifest(ctx)
	now := time.Now()
	if err != nil {
		log.Printf("update: check failed: %v", err)
		setUpdate(func(s *updateState) {
			s.State, s.Message, s.Checked = "error", "Couldn't check for updates: "+err.Error(), now
			if errors.Is(err, errNoRelease) {
				s.State, s.Message = "current", ""
			}
		})
		return err
	}
	_, hasAsset := m.Assets[runtime.GOOS]
	if !update.IsNewer(m.Version, version) || !hasAsset {
		setUpdate(func(s *updateState) { s.State, s.Latest, s.Checked, s.NotesURL = "current", m.Version, now, "" })
		upd.Lock()
		upd.m = nil
		upd.Unlock()
		return nil
	}
	upd.Lock()
	upd.m = m
	upd.s.State, upd.s.Latest, upd.s.NotesURL, upd.s.Checked = "available", m.Version, m.NotesURL, now
	first := upd.prefs.Notified != m.Version
	if first {
		upd.prefs.Notified = m.Version
		saveUpdatePrefs()
	}
	upd.Unlock()
	log.Printf("update: %s is available (this is %s)", m.Version, version)
	if first {
		notify("HopDesk "+m.Version+" is available", "Open HopDesk to install it.")
	}
	return nil
}

func fetchManifest(ctx context.Context) (*update.Manifest, error) {
	data, err := fetchSmall(ctx, updateBaseURL+"/latest/download/latest.json", 64<<10)
	if err != nil {
		return nil, err
	}
	sig, err := fetchSmall(ctx, updateBaseURL+"/latest/download/latest.json.sig", 1<<10)
	if err != nil {
		if errors.Is(err, errNoRelease) {
			return nil, errors.New("the latest release isn't signed")
		}
		return nil, err
	}
	return update.Verify(updateKey, data, string(sig))
}

// installAvailableUpdate downloads, verifies and installs the update found by
// the last check. On success the app restarts as the new version.
func installAvailableUpdate() error {
	upd.Lock()
	m := upd.m
	state := upd.s.State
	upd.Unlock()
	if m == nil || state != "available" {
		return errors.New("there is no update to install; check again")
	}
	fail := func(err error) error {
		log.Printf("update: %v", err)
		// Stay "available" so Install update can be tried again.
		setUpdate(func(s *updateState) {
			s.State, s.Progress, s.Message = "available", 0, "The update didn't install: "+err.Error()
		})
		return err
	}
	setUpdate(func(s *updateState) { s.State, s.Message, s.Progress = "downloading", "", 0 })
	file, err := downloadAsset(m)
	if err != nil {
		return fail(err)
	}
	setUpdate(func(s *updateState) { s.State, s.Progress = "installing", 100 })
	log.Printf("update: installing %s", m.Version)
	if err := installUpdate(file); err != nil {
		return fail(err)
	}
	return nil
}

func downloadAsset(m *update.Manifest) (string, error) {
	a := m.Assets[runtime.GOOS]
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", updateBaseURL+"/download/"+m.Version+"/"+a.Name, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "HopDesk/"+version)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download failed: GitHub answered %s", resp.Status)
	}
	dir, err := os.MkdirTemp("", "hopdesk-update-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, a.Name) // a.Name is a validated plain file name
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	pw := &progressWriter{total: a.Size}
	n, err := io.Copy(io.MultiWriter(f, h, pw), io.LimitReader(resp.Body, a.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("download failed: %w", err)
	}
	if n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		os.RemoveAll(dir)
		return "", errors.New("the downloaded file doesn't match the signed checksum, so it wasn't installed")
	}
	return path, nil
}

type progressWriter struct {
	total, done int64
	last        int
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if pct := int(p.done * 100 / p.total); pct != p.last && pct <= 100 {
		p.last = pct
		setUpdate(func(s *updateState) { s.Progress = pct })
	}
	return len(b), nil
}

// startUpdateLoop checks shortly after start, then about once a day.
func startUpdateLoop() {
	loadUpdatePrefs()
	go func() {
		time.Sleep(45 * time.Second)
		for {
			upd.Lock()
			auto := !upd.prefs.NoAutoCheck
			upd.Unlock()
			if auto {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				_ = checkForUpdate(ctx)
				cancel()
			}
			time.Sleep(23*time.Hour + time.Duration(rand.IntN(120))*time.Minute)
		}
	}()
}
