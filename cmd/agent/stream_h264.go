package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// H.264 pipeline: raw RGBA frames -> ffmpeg (stdin) -> RTP over a local UDP
// socket -> pion track -> viewer. ffmpeg does the encoding and packetizing;
// we only rewrite sequence numbers and timestamps so the stream stays
// continuous when the encoder restarts (after a settings change).

var (
	encOnce sync.Once
	encName string
)

// h264Encoder returns the ffmpeg encoder to use, or "" if H.264 isn't available.
func h264Encoder() string {
	encOnce.Do(func() {
		path := findFFmpeg()
		if path == "" {
			log.Printf("video: ffmpeg not found, using JPEG frames (install ffmpeg for smoother H.264 video)")
			return
		}
		*ffmpegPath = path
		out, err := exec.Command(*ffmpegPath, "-hide_banner", "-encoders").Output()
		if err != nil {
			log.Printf("video: could not list ffmpeg encoders (%v), using JPEG frames", err)
			return
		}
		list := string(out)
		cands := []string{"libx264"}
		switch runtime.GOOS {
		case "darwin":
			cands = []string{"h264_videotoolbox", "libx264"}
		case "windows":
			cands = []string{"libx264", "h264_mf"}
		}
		if *encoderFlag != "" {
			cands = []string{*encoderFlag}
		}
		for _, c := range cands {
			if strings.Contains(list, " "+c+" ") {
				encName = c
				break
			}
		}
		if encName == "" {
			log.Printf("video: ffmpeg has none of %v, using JPEG frames", cands)
			return
		}
		log.Printf("video: H.264 with ffmpeg encoder %s", encName)
	})
	return encName
}

// findFFmpeg looks next to HopDesk (the Windows installer puts ffmpeg.exe
// there), then on PATH, then where Homebrew installs it: apps opened from
// Finder don't get the shell's PATH.
func findFFmpeg() string {
	if *ffmpegPath != "ffmpeg" {
		if p, err := exec.LookPath(*ffmpegPath); err == nil {
			return p
		}
		return ""
	}
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name = "ffmpeg.exe"
	}
	var cands []string
	if exe, err := os.Executable(); err == nil {
		cands = append(cands, filepath.Join(filepath.Dir(exe), name))
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		cands = append(cands, p)
	}
	cands = append(cands, "/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg")
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}

// ffmpegArgs builds the encoder command. high selects H.264 High profile
// (sharper at the same bitrate) for viewers whose browser decodes it.
func ffmpegArgs(w, h, fps int, pr preset, high bool, port int) []string {
	br := pr.bitrateFor(fps)
	profile := "baseline"
	if high {
		profile = "high"
	}
	a := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "rawvideo", "-pix_fmt", "rgba", "-s", fmt.Sprintf("%dx%d", w, h),
		"-use_wallclock_as_timestamps", "1", "-i", "pipe:0",
		"-an", "-fps_mode", "passthrough",
		"-c:v", encName, "-pix_fmt", "yuv420p",
		"-g", strconv.Itoa(fps * 2), "-bf", "0",
		"-b:v", strconv.Itoa(br), "-maxrate", strconv.Itoa(br), "-bufsize", strconv.Itoa(br * 2 / 3),
	}
	switch encName {
	case "libx264":
		a = append(a, "-preset", pr.x264, "-tune", "zerolatency", "-profile:v", profile)
	case "h264_videotoolbox":
		a = append(a, "-realtime", "1", "-profile:v", profile, "-allow_sw", "1")
	case "h264_mf":
		a = append(a, "-scenario", "display_remoting", "-rate_control", "ld_vbr")
	}
	return append(a,
		// repeat SPS/PPS before every keyframe so the browser can always start decoding
		"-bsf:v", "dump_extra=freq=keyframe",
		"-f", "rtp", "-payload_type", "96",
		fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1200", port))
}

// viewerDecodesHigh reports whether the viewer's offer lists H.264
// Constrained High (Chrome, Edge, Safari do; Firefox doesn't).
func viewerDecodesHigh(offerSDP string) bool {
	return highProfileRe.MatchString(offerSDP)
}

var highProfileRe = regexp.MustCompile(`(?i)profile-level-id=640c`)

type rtpState struct {
	started    bool
	needOffset bool
	seqOff     uint16
	tsOff      uint32
	lastSeq    uint16
	lastTS     uint32
	fps        int
}

var errRestart = errors.New("restart encoder")

func runH264(s *session) {
	failures := 0
	for s.ctx.Err() == nil {
		start := time.Now()
		err := encodeOnce(s)
		if s.ctx.Err() != nil {
			return
		}
		if errors.Is(err, errRestart) {
			failures = 0
			continue
		}
		log.Printf("session %s: H.264 encoder stopped: %v", s.short(), err)
		if time.Since(start) < 5*time.Second {
			failures++
		} else {
			failures = 0
		}
		if failures >= 3 {
			log.Printf("session %s: switching to JPEG frames", s.short())
			s.fallbackToJPEG()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// tailBuffer keeps the last bytes ffmpeg wrote to stderr, for error messages.
type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 2000 {
		t.b = t.b[len(t.b)-2000:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.b))
}

// encodeOnce runs one ffmpeg process until it fails, the session ends, or
// the settings/resolution change (errRestart).
func encodeOnce(s *session) error {
	set, ver := s.getSettings()
	pr := presets[set.Quality]
	img, err := captureFrame(set.Display, pr.maxW)
	if err != nil {
		return err
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return err
	}
	_ = conn.SetReadBuffer(1 << 20)
	port := conn.LocalAddr().(*net.UDPAddr).Port

	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, *ffmpegPath, ffmpegArgs(w, h, set.FPS, pr, s.h264High, port)...)
	stderr := &tailBuffer{}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		conn.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		conn.Close()
		return err
	}

	s.rtp.needOffset = true
	s.rtp.fps = set.FPS
	fwdDone := make(chan error, 1)
	go func() { fwdDone <- forwardRTP(conn, s.track, &s.rtp) }()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	frames := make(chan []byte, 1)
	writeErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case f := <-frames:
				if _, err := stdin.Write(f); err != nil {
					writeErr <- err
					return
				}
			}
		}
	}()
	offer := func(img []byte) {
		select {
		case frames <- img:
		default: // encoder busy: drop this frame
		}
	}
	offer(img.Pix)

	t := time.NewTicker(time.Second / time.Duration(set.FPS))
	defer t.Stop()
	var result error
loop:
	for {
		select {
		case <-ctx.Done():
			result = ctx.Err()
			break loop
		case err := <-exited:
			result = fmt.Errorf("ffmpeg exited: %v", err)
			exited <- err
			break loop
		case err := <-writeErr:
			result = fmt.Errorf("writing frames: %v", err)
			break loop
		case <-t.C:
			if _, v := s.getSettings(); v != ver {
				result = errRestart
				break loop
			}
			img, err := captureFrame(set.Display, pr.maxW)
			if err != nil {
				continue
			}
			if img.Rect.Dx() != w || img.Rect.Dy() != h {
				result = errRestart
				break loop
			}
			offer(img.Pix)
		}
	}

	_ = stdin.Close()
	cancel()
	<-exited
	conn.Close()
	<-fwdDone
	if !errors.Is(result, errRestart) && !errors.Is(result, context.Canceled) {
		if msg := stderr.String(); msg != "" {
			result = fmt.Errorf("%v: %s", result, msg)
		}
	}
	return result
}

// forwardRTP reads ffmpeg's RTP packets and writes them to the WebRTC track,
// shifting sequence numbers and timestamps to continue the previous run.
func forwardRTP(conn *net.UDPConn, track *webrtc.TrackLocalStaticRTP, st *rtpState) error {
	buf := make([]byte, 1600)
	var sender *net.UDPAddr // ffmpeg's address; packets from anyone else are ignored
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return err
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
		if st.needOffset {
			if st.started {
				st.seqOff = st.lastSeq + 1 - p.SequenceNumber
				st.tsOff = st.lastTS + uint32(90000/max(1, st.fps)) - p.Timestamp
			}
			st.needOffset, st.started = false, true
		}
		p.SequenceNumber += st.seqOff
		p.Timestamp += st.tsOff
		st.lastSeq, st.lastTS = p.SequenceNumber, p.Timestamp
		if err := track.WriteRTP(&p); err != nil && errors.Is(err, io.ErrClosedPipe) {
			return err
		}
	}
}
