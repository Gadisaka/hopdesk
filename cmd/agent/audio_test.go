//go:build !darwin && !windows

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func hubState(h *audioHub) (subs int, running, stopping bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs), h.cancel != nil, h.stop != nil
}

func TestAudioHubListeners(t *testing.T) {
	h := &audioHub{subs: map[*session]*webrtc.TrackLocalStaticRTP{}}
	a := &session{id: "aaaaaaaa", audio: &webrtc.TrackLocalStaticRTP{}}
	b := &session{id: "bbbbbbbb", audio: &webrtc.TrackLocalStaticRTP{}}
	noTrack := &session{id: "cccccccc"}

	h.listen(noTrack, true)
	if n, run, _ := hubState(h); n != 0 || run {
		t.Fatalf("a session without a sound track must not start capture (subs %d, running %v)", n, run)
	}

	h.listen(a, true)
	h.listen(b, true)
	if n, run, _ := hubState(h); n != 2 || !run {
		t.Fatalf("want 2 listeners and a running capture, got %d, %v", n, run)
	}
	h.listen(a, false)
	if n, _, stopping := hubState(h); n != 1 || stopping {
		t.Fatalf("one listener left: capture must keep going (subs %d, stopping %v)", n, stopping)
	}
	h.listen(b, false)
	if n, _, stopping := hubState(h); n != 0 || !stopping {
		// On this platform capture fails straight away, so run may already have given up.
		if _, run, _ := hubState(h); run {
			t.Fatalf("last listener left: want a pending stop (subs %d)", n)
		}
	}
	h.listen(b, true)
	if _, _, stopping := hubState(h); stopping {
		t.Fatal("turning sound back on must cancel the pending stop")
	}
	h.listen(b, false)
}

func TestAudioHubGivesUp(t *testing.T) {
	h := &audioHub{subs: map[*session]*webrtc.TrackLocalStaticRTP{}}
	s := &session{id: "dddddddd", audio: &webrtc.TrackLocalStaticRTP{}}
	h.listen(s, true) // capture isn't supported here, so every attempt fails
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, run, _ := hubState(h); !run {
			h.listen(s, false)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("capture kept retrying after repeated failures")
}

func TestOpusArgs(t *testing.T) {
	opusOnce.Do(func() {})
	defer func() { opusEnc = "" }()
	for _, enc := range []string{"libopus", "opus"} {
		opusEnc = enc
		args := strings.Join(opusArgs(audioFormat{Rate: 44100, Channels: 1, Sample: "s16le"}, 5004), " ")
		for _, want := range []string{"-f s16le -ar 44100 -ac 1 -i pipe:0", "-c:a " + enc, "-ar 48000 -ac 2", "-payload_type 111", "rtp://127.0.0.1:5004"} {
			if !strings.Contains(args, want) {
				t.Errorf("%s: %q missing from %s", enc, want, args)
			}
		}
	}
}
