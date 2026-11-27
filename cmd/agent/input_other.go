//go:build !darwin && !windows

package main

import "errors"

func platformInit() error {
	return errors.New("remote input is only implemented for macOS and Windows; this agent will be view-only")
}

func inputMove(x, y float64)                                 {}
func inputButton(x, y float64, b int, down bool, clicks int) {}
func inputScroll(dx, dy float64)                             {}
func inputKey(code, key string, down bool)                   {}
