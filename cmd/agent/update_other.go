//go:build !darwin && !windows

package main

func installUpdate(string) error { return errUnsupported }
