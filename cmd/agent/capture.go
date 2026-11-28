package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/jpeg"
	"log"
	"sync"
	"time"

	"github.com/kbinani/screenshot"
	"github.com/pion/webrtc/v4"
	"golang.org/x/image/draw"
)

const chunkSize = 16000 // stays under every browser's data-channel message limit

type boundsEntry struct {
	r  image.Rectangle
	at time.Time
}

var (
	boundsMu    sync.Mutex
	boundsCache = map[int]boundsEntry{}
)

func initCapture() error {
	n := screenshot.NumActiveDisplays()
	if n == 0 {
		return fmt.Errorf("no displays found (on macOS, allow Screen Recording for this app)")
	}
	return nil
}

func listDisplays() []image.Rectangle {
	n := screenshot.NumActiveDisplays()
	out := make([]image.Rectangle, n)
	for i := range out {
		out[i] = displayBounds(i)
	}
	return out
}

// displayBounds returns a display's position in the OS's global coordinate
// space, refreshed every few seconds in case the resolution changes.
func displayBounds(d int) image.Rectangle {
	boundsMu.Lock()
	defer boundsMu.Unlock()
	e, ok := boundsCache[d]
	if !ok || time.Since(e.at) > 3*time.Second {
		e = boundsEntry{screenshot.GetDisplayBounds(d), time.Now()}
		boundsCache[d] = e
	}
	return e.r
}

// captureFrame grabs a display and returns a tightly packed RGBA image at
// most maxW wide, with even dimensions (required by the H.264 encoder).
func captureFrame(d, maxW int) (*image.RGBA, error) {
	img, err := captureRect(displayBounds(d))
	if err != nil {
		return nil, err
	}
	sw, sh := img.Rect.Dx(), img.Rect.Dy()
	if sw < 2 || sh < 2 {
		return nil, fmt.Errorf("empty capture")
	}
	w, h := sw, sh
	if maxW <= 0 {
		maxW = maxNativeWidth
	}
	if w > maxW {
		h = h * maxW / w
		w = maxW
	}
	w, h = w&^1, h&^1
	if w != sw || h != sh {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		// BiLinear keeps small text much crisper than ApproxBiLinear.
		draw.BiLinear.Scale(dst, dst.Rect, img, img.Rect, draw.Src, nil)
		return dst, nil
	}
	if img.Rect.Min != (image.Point{}) || img.Stride != 4*w {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(dst, dst.Rect, img, img.Rect.Min, draw.Src)
		return dst, nil
	}
	return img, nil
}

// runJPEG sends JPEG frames as chunks: [u32 frameId][u16 index][u16 count][bytes].
// Unchanged frames are skipped, and frames are dropped while the channel is
// backed up, so a slow link lowers the frame rate instead of adding lag.
func runJPEG(s *session, dc *webrtc.DataChannel) {
	set, ver := s.getSettings()
	t := time.NewTicker(time.Second / time.Duration(set.FPS))
	defer t.Stop()

	var frameID, lastSum uint32
	var buf bytes.Buffer
	force := true

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		if dc.ReadyState() != webrtc.DataChannelStateOpen {
			return
		}
		if cur, v := s.getSettings(); v != ver {
			if cur.FPS != set.FPS {
				t.Reset(time.Second / time.Duration(cur.FPS))
			}
			set, ver, force = cur, v, true
		}
		if dc.BufferedAmount() > 1<<20 {
			continue
		}
		pr := presets[set.Quality]
		img, err := captureFrame(set.Display, pr.maxW)
		if err != nil {
			log.Printf("capture: %v", err)
			time.Sleep(time.Second)
			continue
		}
		sum := crc32.ChecksumIEEE(img.Pix)
		if !force && sum == lastSum {
			continue
		}
		lastSum, force = sum, false

		buf.Reset()
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: pr.jpegQ}); err != nil {
			continue
		}
		data := buf.Bytes()
		count := (len(data) + chunkSize - 1) / chunkSize
		if count > 0xFFFF {
			continue
		}
		frameID++
		for i := 0; i < count; i++ {
			end := min(len(data), (i+1)*chunkSize)
			msg := make([]byte, 8+end-i*chunkSize)
			binary.BigEndian.PutUint32(msg[0:4], frameID)
			binary.BigEndian.PutUint16(msg[4:6], uint16(i))
			binary.BigEndian.PutUint16(msg[6:8], uint16(count))
			copy(msg[8:], data[i*chunkSize:end])
			if err := dc.Send(msg); err != nil {
				return
			}
		}
	}
}
