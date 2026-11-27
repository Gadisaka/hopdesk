package main

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Optional Tailscale helpers for "this computer is the server": find this
// computer's Tailscale name and turn on HTTPS with `tailscale serve`.

func tailscaleCLI() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	var cands []string
	switch runtime.GOOS {
	case "darwin":
		cands = []string{"/Applications/Tailscale.app/Contents/MacOS/Tailscale", "/usr/local/bin/tailscale", "/opt/homebrew/bin/tailscale"}
	case "windows":
		cands = []string{`C:\Program Files\Tailscale\tailscale.exe`}
	}
	for _, c := range cands {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return ""
}

var tsCache struct {
	sync.Mutex
	name string
	at   time.Time
}

// tailscaleDNSName returns e.g. "mymac.tail1234.ts.net", cached for 30 s.
func tailscaleDNSName() (string, error) {
	tsCache.Lock()
	defer tsCache.Unlock()
	if time.Since(tsCache.at) < 30*time.Second {
		return tsCache.name, nil
	}
	tsCache.at = time.Now()
	tsCache.name = ""
	cli := tailscaleCLI()
	if cli == "" {
		return "", errors.New("tailscale not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, cli, "status", "--json").Output()
	if err != nil {
		return "", err
	}
	var st struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return "", err
	}
	tsCache.name = strings.TrimSuffix(st.Self.DNSName, ".")
	return tsCache.name, nil
}

// tailscaleServe makes https://<this computer>.ts.net proxy to the built-in server.
func tailscaleServe(port string) error {
	cli := tailscaleCLI()
	if cli == "" {
		return errors.New("Tailscale isn't installed on this computer")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, cli, "serve", "--bg", port).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "HTTPS") || strings.Contains(msg, "https") {
			msg += " (turn on MagicDNS and HTTPS certificates in the Tailscale admin console)"
		}
		return errors.New(msg)
	}
	tsCache.Lock()
	tsCache.at = time.Time{}
	tsCache.Unlock()
	return nil
}
