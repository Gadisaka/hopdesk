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
	encHW   bool // h264_mf: use the graphics chip (Media Foundation hardware encoder)
)

// ffmpegCmd runs ffmpeg without a console window popping up (Windows).
func ffmpegCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, *ffmpegPath, args...)
	hideConsole(cmd)
	return cmd
}

// encoderChoice is one way to encode H.264, best first in encoderChoices.
type encoderChoice struct {
	codec string
	hw    bool // h264_mf only: ask for the hardware encoder
	probe bool // only use it if a quick test encode works on this computer
}

// encoderChoices: encoders on the graphics chip first (fast, and leave the
// processor free), then software. ffmpeg lists hardware encoders whether or
// not this computer has the chip, so those are tested before use.
func encoderChoices() []encoderChoice {
	switch runtime.GOOS {
	case "darwin":
		return []encoderChoice{{codec: "h264_videotoolbox"}, {codec: "libx264"}}
	case "windows":
		return []encoderChoice{
			{codec: "h264_nvenc", probe: true}, // NVIDIA
			{codec: "h264_qsv", probe: true},   // Intel Quick Sync
			{codec: "h264_amf", probe: true},   // AMD
			{codec: "h264_mf", hw: true, probe: true},
			{codec: "libx264"},
			{codec: "h264_mf"}, // Windows' software encoder: works everywhere, but slow
		}
	}
	return []encoderChoice{{codec: "libx264"}}
}

// h264Encoder returns the ffmpeg encoder to use, or "" if H.264 isn't available.
func h264Encoder() string {
	encOnce.Do(func() {
		path := findFFmpeg()
		if path == "" {
			log.Printf("video: ffmpeg not found, using JPEG frames (install ffmpeg for smoother H.264 video)")
			return
		}
		*ffmpegPath = path
		out, err := ffmpegCmd(context.Background(), "-hide_banner", "-encoders").Output()
		if err != nil {
			log.Printf("video: could not list ffmpeg encoders (%v), using JPEG frames", err)
			return
		}
		list := string(out)
		cands := encoderChoices()
		if *encoderFlag != "" {
			cands = []encoderChoice{{codec: *encoderFlag}}
		}
		for _, c := range cands {
			if !strings.Contains(list, " "+c.codec+" ") {
				continue
			}
			if c.probe {
				if err := testEncoder(c); err != nil {
					log.Printf("video: %s isn't usable here (%v)", encoderLabel(c.codec, c.hw), err)
					continue
				}
			}
			encName, encHW = c.codec, c.hw
			break
		}
		if encName == "" {
			log.Printf("video: ffmpeg has no usable H.264 encoder, using JPEG frames")
			return
		}
		log.Printf("video: H.264 with %s", encoderLabel(encName, encHW))
	})
	return encName
}

// encoderLabel names an encoder for the log and the settings page.
func encoderLabel(codec string, hw bool) string {
	switch {
	case codec == "h264_mf" && hw:
		return "h264_mf, graphics chip"
	case codec == "h264_mf":
		return "h264_mf, software"
	}
	return codec
}

// testEncoder encodes a few frames, to find out whether a hardware encoder
// actually works on this computer (right chip, driver and permissions).
func testEncoder(c encoderChoice) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "color=c=gray:s=1280x720:r=30", "-frames:v", "10",
		"-c:v", c.codec, "-pix_fmt", encoderPixFmt(c.codec)}
	args = append(args, encoderOpts(c.codec, c.hw, "high")...)
	args = append(args, "-f", "null", "-")
	out, err := ffmpegCmd(ctx, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = strings.TrimSpace(msg[:i]) // the first line says why (e.g. "Cannot load libcuda")
		}
		if len(msg) > 160 {
			msg = msg[:160]
		}
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

// encoderPixFmt: the hardware encoders take NV12; the software ones YUV 4:2:0.
func encoderPixFmt(codec string) string {
	switch codec {
	case "h264_qsv", "h264_amf", "h264_mf":
		return "nv12"
	}
	return "yuv420p"
}

// encoderOpts: low-latency settings for each encoder.
func encoderOpts(codec string, hw bool, profile string) []string {
	switch codec {
	case "libx264":
		return nil // set in ffmpegArgs (depends on the quality preset)
	case "h264_videotoolbox":
		return []string{"-realtime", "1", "-profile:v", profile, "-allow_sw", "1"}
	case "h264_nvenc":
		return []string{"-preset", "p2", "-tune", "ull", "-zerolatency", "1", "-rc", "cbr", "-profile:v", profile}
	case "h264_qsv":
		return []string{"-preset", "veryfast", "-look_ahead", "0", "-async_depth", "1", "-profile:v", profile}
	case "h264_amf":
		amfProfile := profile
		if profile == "baseline" {
			amfProfile = "constrained_baseline"
		}
		return []string{"-usage", "ultralowlatency", "-quality", "speed", "-rc", "cbr", "-profile:v", amfProfile}
	case "h264_mf":
		o := []string{"-scenario", "display_remoting", "-rate_control", "ld_vbr"}
		if hw {
			o = append(o, "-hw_encoding", "1")
		}
		return o
	}
	return nil
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
		"-c:v", encName, "-pix_fmt", encoderPixFmt(encName),
		"-g", strconv.Itoa(fps * 2), "-bf", "0",
		"-b:v", strconv.Itoa(br), "-maxrate", strconv.Itoa(br), "-bufsize", strconv.Itoa(br * 2 / 3),
	}
	if encName == "libx264" {
		a = append(a, "-preset", pr.x264, "-tune", "zerolatency", "-profile:v", profile)
	} else {
		a = append(a, encoderOpts(encName, encHW, profile)...)
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
	cmd := ffmpegCmd(ctx, ffmpegArgs(w, h, set.FPS, pr, s.h264High, port)...)
	log.Printf("session %s: encoding %dx%d at %d fps with %s", s.short(), w, h, set.FPS, encoderLabel(encName, encHW))
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
