//go:build !windows

package main

import (
	"image"

	"github.com/kbinani/screenshot"
)

// onInputDesktop runs f. Only Windows has separate desktops to follow.
func onInputDesktop(f func()) { f() }

func captureRect(r image.Rectangle) (*image.RGBA, error) { return screenshot.CaptureRect(r) }
