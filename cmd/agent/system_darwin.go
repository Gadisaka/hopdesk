//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func openURL(u string) { _ = exec.Command("open", u).Start() }

// appBundle returns /Applications/HopDesk.app when running from a bundle.
func appBundle() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if i := strings.Index(exe, ".app/Contents/MacOS/"); i > 0 {
		return exe[:i+4]
	}
	return ""
}

func restartSelf() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = syscall.Exec(exe, []string{exe, "-app", "-restarted"}, os.Environ())
}

const launchLabel = "com.hopdesk.desktop"

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist")
}

func autostartSupported() bool { return true }

// setAutostart adds or removes a LaunchAgent that opens HopDesk at login.
func setAutostart(on bool) error {
	p := launchAgentPath()
	if !on {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	args := ""
	if b := appBundle(); b != "" {
		args = "<string>/usr/bin/open</string><string>-a</string><string>" + xmlEscape(b) + "</string>"
	} else {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		args = "<string>" + xmlEscape(exe) + "</string><string>-app</string>"
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array>%s</array>
  <key>RunAtLoad</key><true/>
  <key>LimitLoadToSessionType</key><string>Aqua</string>
</dict></plist>
`, launchLabel, args)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(plist), 0o644)
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
