package main

// Sound: system audio (audio_darwin.go, audio_windows.go) -> ffmpeg (Opus) ->
// RTP over a local UDP socket -> every listening viewer's audio track.
//
// One capture serves every viewer, and it runs only while at least one of
// them has sound turned on (the viewer starts muted and sends {"t":"audio"}).
// When nothing is playing, silence is filled in so the stream's clock keeps
// running and the sound stays in step with the picture.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

var audioFlag = flag.Bool("audio", true, "let viewers hear this computer's sound")

// audioFormat describes the raw samples a capture produces.
type audioFormat struct {
	Rate     int
	Channels int
	Sample   string // ffmpeg raw format: f32le, s16le or s32le
}

func (f audioFormat) bytesPerFrame() int {
	n := 4
	if f.Sample == "s16le" {
		n = 2
	}
	return n * f.Channels
}

var (
	opusOnce sync.Once
	opusEnc  string
)

// opusEncoder returns the ffmpeg Opus encoder to use, or "".
func opusEncoder() string {
	opusOnce.Do(func() {
		path := findFFmpeg()
		if path == "" {
			return
		}
		out, err := exec.Command(path, "-hide_banner", "-encoders").Output()
		if err != nil {
			return
		}
		list := string(out)
		switch {
		case strings.Contains(list, " libopus "):
			opusEnc = "libopus"
		case strings.Contains(list, " opus "):
			opusEnc = "opus"
		}
	})
	return opusEnc
}

// audioAvailable reports whether viewers can be offered sound, and if not, why.
func audioAvailable() (bool, string) {
	switch {
	case !*audioFlag:
		return false, "turned off"
	case !audioSupported():
		return false, "not supported on this system"
	case opusEncoder() == "":
		return false, "needs ffmpeg with an Opus encoder"
	}
	return true, ""
}

func audioDescription() string {
	if ok, why := audioAvailable(); !ok {
		return "Off (" + why + ")"
	}
	return "Opus (" + opusEncoder() + ")"
}

// ---- the shared capture ----

type audioHub struct {
	mu     sync.Mutex
	subs   map[*session]*webrtc.TrackLocalStaticRTP
	cancel context.CancelFunc
	stop   *time.Timer
}

var sound = &audioHub{subs: map[*session]*webrtc.TrackLocalStaticRTP{}}

// listen turns sound on or off for one session.
func (h *audioHub) listen(s *session, on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if on && s.audio != nil {
		if _, already := h.subs[s]; !already {
			log.Printf("session %s: sound on", s.short())
		}
		h.subs[s] = s.audio
		if h.stop != nil {
			h.stop.Stop()
			h.stop = nil
		}
		if h.cancel == nil {
			ctx, cancel := context.WithCancel(context.Background())
			h.cancel = cancel
			go h.run(ctx)
		}
		return
	}
	if _, was := h.subs[s]; was {
		delete(h.subs, s)
		log.Printf("session %s: sound off", s.short())
	}
	if len(h.subs) == 0 && h.cancel != nil && h.stop == nil {
		// Keep the capture warm for a moment, in case sound is turned straight back on.
		h.stop = time.AfterFunc(10*time.Second, func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if len(h.subs) == 0 && h.cancel != nil {
				h.cancel()
				h.cancel = nil
			}
			h.stop = nil
		})
	}
}

func (h *audioHub) tracks() []*webrtc.TrackLocalStaticRTP {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*webrtc.TrackLocalStaticRTP, 0, len(h.subs))
	for _, t := range h.subs {
		out = append(out, t)
	}
	return out
}

// run keeps the capture and encoder going until ctx ends, restarting after errors.
func (h *audioHub) run(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		start := time.Now()
		err := h.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		log.Printf("sound: stopped: %v", err)
		if time.Since(start) < 10*time.Second {
			failures++
		} else {
			failures = 0
		}
		if failures >= 3 {
			log.Printf("sound: giving up after repeated errors")
			h.mu.Lock()
			if ctx.Err() == nil { // still the current capture (not already stopped and replaced)
				h.cancel()
				h.cancel = nil
			}
			h.mu.Unlock()
			return
		}
		time.Sleep(time.Second)
	}
}

func (h *audioHub) runOnce(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return err
	}
	defer conn.Close()
	port := conn.LocalAddr().(*net.UDPAddr).Port

	// Start capturing first: on Windows the format is only known then.
	pcm := make(chan []byte, 64)
	format, err := startAudioCapture(ctx, func(b []byte) {
		select {
		case pcm <- b:
		default: // the encoder fell behind: drop rather than build up delay
		}
	})
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	log.Printf("sound: capturing %d Hz, %d channels (%s)", format.Rate, format.Channels, format.Sample)

	cmd := exec.CommandContext(ctx, findFFmpeg(), opusArgs(format, port)...)
	stderr := &tailBuffer{}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	go h.forward(conn)

	frameBytes := format.bytesPerFrame()
	perSecond := format.Rate * frameBytes
	silence := make([]byte, perSecond/50) // 20 ms
	began := time.Now()
	var written int64
	write := func(b []byte) error {
		_, err := stdin.Write(b)
		written += int64(len(b))
		return err
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = stdin.Close()
			<-exited
			return ctx.Err()
		case err := <-exited:
			return fmt.Errorf("ffmpeg exited: %v: %s", err, stderr.String())
		case b := <-pcm:
			due := int64(time.Since(began).Seconds() * float64(perSecond))
			if written > due+int64(perSecond)/5 {
				continue // more than 200 ms ahead (after filling silence): drop to stay in step
			}
			if err := write(b[:len(b)/frameBytes*frameBytes]); err != nil {
				return err
			}
		case <-tick.C:
			// Nothing playing (Windows sends no data then): keep the clock running with silence.
			due := int64(time.Since(began).Seconds() * float64(perSecond))
			for written+int64(len(silence)) < due-int64(perSecond)/10 {
				if err := write(silence); err != nil {
					return err
				}
			}
		}
	}
}

// forward sends ffmpeg's RTP packets to every listening viewer.
func (h *audioHub) forward(conn *net.UDPConn) {
	buf := make([]byte, 1600)
	var sender *net.UDPAddr
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if !from.IP.IsLoopback() {
			continue
		}
		if sender == nil {
			sender = from
		} else if from.Port != sender.Port {
			continue
		}
		var p rtp.Packet
		if p.Unmarshal(buf[:n]) != nil {
			continue
		}
		for _, t := range h.tracks() {
			if err := t.WriteRTP(&p); err != nil && !errors.Is(err, net.ErrClosed) {
				continue
			}
		}
	}
}

func opusArgs(f audioFormat, port int) []string {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", f.Sample, "-ar", fmt.Sprint(f.Rate), "-ac", fmt.Sprint(f.Channels), "-i", "pipe:0",
		"-vn", "-c:a", opusEncoder(), "-b:a", "128k", "-ar", "48000", "-ac", "2",
	}
	if opusEncoder() == "libopus" {
		args = append(args, "-application", "lowdelay", "-frame_duration", "20", "-vbr", "on")
	} else {
		args = append(args, "-strict", "-2")
	}
	return append(args, "-payload_type", "111", "-f", "rtp", fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1200", port))
}

// addAudioTrack offers sound to a viewer whose offer asked for it.
func addAudioTrack(s *session, pc *webrtc.PeerConnection, offerSDP string) {
	if ok, _ := audioAvailable(); !ok || !strings.Contains(offerSDP, "m=audio") {
		return
	}
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
		SDPFmtpLine: "minptime=10;useinbandfec=1;stereo=1;sprop-stereo=1",
	}, "audio", "hopdesk-audio")
	if err != nil {
		return
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		return
	}
	s.audio = track
	go func() { // read RTCP so pion's interceptors keep working
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()
}
