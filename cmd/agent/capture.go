package main

import (
	"bytes"
	"context"
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

var (
	boundsMu     sync.Mutex
	cachedBounds image.Rectangle
	boundsAt     time.Time
)

func initCapture() error {
	n := screenshot.NumActiveDisplays()
	if n == 0 {
		return fmt.Errorf("no displays found (on macOS, allow Screen Recording for this app)")
	}
	if *display < 0 || *display >= n {
		return fmt.Errorf("display %d not found (have %d)", *display, n)
	}
	return nil
}

// displayBounds returns the shared display's position in the OS's global
// coordinate space, refreshed every few seconds in case resolution changes.
func displayBounds() image.Rectangle {
	boundsMu.Lock()
	defer boundsMu.Unlock()
	if time.Since(boundsAt) > 3*time.Second {
		cachedBounds = screenshot.GetDisplayBounds(*display)
		boundsAt = time.Now()
	}
	return cachedBounds
}

func captureFrame() (*image.RGBA, error) {
	img, err := screenshot.CaptureRect(displayBounds())
	if err != nil {
		return nil, err
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if *maxWidth > 0 && w > *maxWidth {
		nh := h * *maxWidth / w
		dst := image.NewRGBA(image.Rect(0, 0, *maxWidth, nh))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
		return dst, nil
	}
	return img, nil
}

// streamFrames sends JPEG frames as chunks: [u32 frameId][u16 index][u16 count][bytes].
// Unchanged frames are skipped, and frames are dropped while the channel is
// backed up, so a slow link lowers the frame rate instead of adding lag.
func streamFrames(ctx context.Context, dc *webrtc.DataChannel) {
	interval := time.Second / time.Duration(max(1, *fps))
	t := time.NewTicker(interval)
	defer t.Stop()

	var frameID uint32
	var lastSum uint32
	var buf bytes.Buffer
	opts := &jpeg.Options{Quality: min(100, max(1, *quality))}

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if dc.ReadyState() != webrtc.DataChannelStateOpen {
			return
		}
		if dc.BufferedAmount() > 1<<20 {
			continue
		}
		img, err := captureFrame()
		if err != nil {
			log.Printf("capture: %v", err)
			time.Sleep(time.Second)
			continue
		}
		sum := crc32.ChecksumIEEE(img.Pix)
		if frameID > 0 && sum == lastSum {
			continue
		}
		lastSum = sum

		buf.Reset()
		if err := jpeg.Encode(&buf, img, opts); err != nil {
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
