package main

import (
	"log"
	"time"

	"github.com/atotto/clipboard"
)

// Text only. Kept under the smallest data-channel message size we rely on.
const maxClip = 60000

// setClipboard puts text from the viewer on this computer's clipboard.
func setClipboard(s *session, text string) {
	if len(text) == 0 || len(text) > maxClip {
		return
	}
	s.mu.Lock()
	s.clipLast = text // don't echo it back to this viewer
	s.mu.Unlock()
	if err := clipboard.WriteAll(text); err != nil {
		log.Printf("clipboard write: %v", err)
	}
}

// watchClipboard sends this computer's clipboard to the viewer whenever it changes.
func watchClipboard(s *session) {
	if cur, err := clipboard.ReadAll(); err == nil {
		s.mu.Lock()
		s.clipLast = cur
		s.mu.Unlock()
	}
	t := time.NewTicker(700 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		cur, err := clipboard.ReadAll()
		if err != nil || cur == "" || len(cur) > maxClip {
			continue
		}
		s.mu.Lock()
		changed := cur != s.clipLast
		s.clipLast = cur
		s.mu.Unlock()
		if changed {
			s.sendCtrl(map[string]any{"t": "clip", "text": cur})
		}
	}
}
