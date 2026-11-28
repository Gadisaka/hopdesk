//go:build darwin

package main

/*
#cgo LDFLAGS: -framework ScreenCaptureKit -framework CoreMedia -framework Foundation -framework AudioToolbox
#include <stdlib.h>
char *hdAudioStart(int token);
void hdAudioStop(void);
*/
import "C"

import (
	"context"
	"errors"
	"log"
	"sync"
	"unsafe"
)

// The Objective-C side is in audio_darwin.m.

func audioSupported() bool { return true }

var macAudio struct {
	sync.Mutex
	out   func([]byte)
	token int
}

// startMu serialises starting and stopping the single ScreenCaptureKit stream.
var startMu sync.Mutex

//export hdAudioData
func hdAudioData(data unsafe.Pointer, n C.int, token C.int) {
	macAudio.Lock()
	out, cur := macAudio.out, macAudio.token
	macAudio.Unlock()
	if out == nil || int(token) != cur || n <= 0 {
		return
	}
	out(C.GoBytes(data, n))
}

//export hdAudioStopped
func hdAudioStopped(token C.int, msg *C.char) {
	log.Printf("sound: macOS stopped the capture: %s", C.GoString(msg))
}

// startAudioCapture captures the system sound until ctx ends, as interleaved
// float32 stereo at 48 kHz.
func startAudioCapture(ctx context.Context, out func([]byte)) (audioFormat, error) {
	startMu.Lock()
	defer startMu.Unlock()
	macAudio.Lock()
	macAudio.token++
	tok := macAudio.token
	macAudio.out = out
	macAudio.Unlock()

	if e := C.hdAudioStart(C.int(tok)); e != nil {
		msg := C.GoString(e)
		C.free(unsafe.Pointer(e))
		macAudio.Lock()
		macAudio.out = nil
		macAudio.Unlock()
		return audioFormat{}, errors.New(msg)
	}
	go func() {
		<-ctx.Done()
		startMu.Lock()
		defer startMu.Unlock()
		macAudio.Lock()
		current := macAudio.token == tok
		if current {
			macAudio.out = nil
		}
		macAudio.Unlock()
		if current { // a newer capture may already have replaced this one
			C.hdAudioStop()
		}
	}()
	return audioFormat{Rate: 48000, Channels: 2, Sample: "f32le"}, nil
}
