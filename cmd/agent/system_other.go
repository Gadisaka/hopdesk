//go:build !darwin && !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func openURL(u string) { _ = exec.Command("xdg-open", u).Start() }

func restartSelf() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = syscall.Exec(exe, []string{exe, "-app", "-restarted"}, os.Environ())
}

func autostartSupported() bool { return false }
func setAutostart(on bool) error {
	if on {
		return errUnsupported
	}
	return nil
}
