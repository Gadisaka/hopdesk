//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

func openURL(u string) {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Start()
}

func restartSelf() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if err := exec.Command(exe, "-app", "-restarted").Start(); err == nil {
		os.Exit(0)
	}
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func autostartSupported() bool { return true }

// setAutostart adds or removes HopDesk from the current user's startup programs.
func setAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue("HopDesk"); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue("HopDesk", `"`+exe+`" -app`)
}

// appBundle is only meaningful on macOS.
func appBundle() string { return "" }
