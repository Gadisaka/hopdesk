//go:build !darwin

package main

// Windows and Linux don't need screen or input permissions for this.
func permSupported() bool      { return false }
func permScreen() bool         { return true }
func permAccessibility() bool  { return true }
func requestPermission(string) {}
