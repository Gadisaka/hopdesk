//go:build windows

package main

import (
	"fmt"
	"log"
	"os/exec"
	"syscall"
	"time"
)

// installUpdate runs the downloaded installer silently. It closes this copy,
// installs over it in the same folder, and starts the new version
// (see /RELAUNCH in packaging/windows/hopdesk.nsi).
func installUpdate(setup string) error {
	cmd := exec.Command(setup, "/S", "/RELAUNCH")
	const detached, newGroup = 0x00000008, 0x00000200 // DETACHED_PROCESS, CREATE_NEW_PROCESS_GROUP
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: detached | newGroup}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("couldn't start the installer: %w", err)
	}
	log.Printf("update: installer started; quitting")
	go func() {
		time.Sleep(time.Second)
		quitApp()
	}()
	return nil
}
