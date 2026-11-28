//go:build !darwin && !windows

package main

import (
	"context"
	"errors"
)

func audioSupported() bool { return false }

func startAudioCapture(context.Context, func([]byte)) (audioFormat, error) {
	return audioFormat{}, errors.New("sound capture isn't available on this system")
}
