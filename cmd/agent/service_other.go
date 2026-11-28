//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
)

// Login-screen control is Windows-only: on a Mac, HopDesk already works
// while a user is logged in, and macOS doesn't let apps control its login
// window.

func serviceSupported() bool                   { return false }
func serviceDataDir() string                   { return "" }
func serviceStatus() (installed, running bool) { return false, false }
func setServiceEnabled(on bool) error          { return errors.New("only available on Windows") }
func sasAvailable() bool                       { return false }
func requestSAS()                              {}
func currentSessionID() int                    { return 0 }
func exitWithProcess(pid int)                  {}

func runServiceCommand(args []string) int {
	fmt.Fprintln(os.Stderr, "the HopDesk service is only available on Windows")
	return 2
}

func userHome() string {
	h, _ := os.UserHomeDir()
	return h
}
