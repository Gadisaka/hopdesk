package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// File transfer uses one data channel per file.
//
// Upload (viewer -> this computer), channel "upload":
//   viewer: {"t":"meta","name":..,"size":..}, binary chunks..., {"t":"end"}
//   agent:  {"t":"ok","path":..} or {"t":"error","message":..}
//
// Download (this computer -> viewer), channel "download":
//   viewer: {"path":..}
//   agent:  {"t":"meta","name":..,"size":..}, binary chunks..., {"t":"end"}  (or {"t":"error"})

const fileChunk = 16000

func receiveDir() string {
	if *uploadDir != "" {
		return *uploadDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

// safeName strips any directory parts and characters that aren't allowed in file names.
func safeName(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." {
		return ""
	}
	return name
}

// createUnique creates dir/name, or "name (1).ext" etc. if it already exists.
func createUnique(dir, name string) (*os.File, string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		n := name
		if i > 0 {
			n = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		p := filepath.Join(dir, n)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return f, p, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("too many files with that name")
}

func sendJSON(dc *webrtc.DataChannel, v any) {
	b, _ := json.Marshal(v)
	_ = dc.SendText(string(b))
}

func handleUpload(s *session, dc *webrtc.DataChannel) {
	var (
		mu        sync.Mutex
		f         *os.File
		p         string
		size, got int64
		done      bool
	)
	cleanup := func() {
		if f != nil {
			f.Close()
			os.Remove(p) // drop the partial file
			f = nil
		}
	}
	fail := func(msg string) {
		cleanup()
		done = true
		sendJSON(dc, map[string]any{"t": "error", "message": msg})
	}

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		mu.Lock()
		defer mu.Unlock()
		if done {
			return
		}
		if !s.isAuthed() {
			done = true
			_ = dc.Close()
			return
		}
		if msg.IsString {
			var m struct {
				T    string `json:"t"`
				Name string `json:"name"`
				Size int64  `json:"size"`
			}
			if json.Unmarshal(msg.Data, &m) != nil {
				return
			}
			switch {
			case m.T == "meta" && f == nil:
				name := safeName(m.Name)
				if name == "" || m.Size < 0 {
					fail("invalid file name")
					return
				}
				var err error
				f, p, err = createUnique(receiveDir(), name)
				if err != nil {
					fail(err.Error())
					return
				}
				size = m.Size
			case m.T == "end" && f != nil:
				if got != size {
					fail("transfer incomplete")
					return
				}
				if err := f.Close(); err != nil {
					f = nil
					os.Remove(p)
					fail(err.Error())
					return
				}
				f = nil
				done = true
				log.Printf("session %s: received %s (%d bytes)", s.short(), p, size)
				sendJSON(dc, map[string]any{"t": "ok", "path": p})
			}
			return
		}
		if f == nil {
			fail("data before file info")
			return
		}
		got += int64(len(msg.Data))
		if got > size {
			fail("more data than announced")
			return
		}
		if _, err := f.Write(msg.Data); err != nil {
			fail(err.Error())
		}
	})
	dc.OnClose(func() {
		mu.Lock()
		defer mu.Unlock()
		cleanup()
		done = true
	})
}

func handleDownload(s *session, dc *webrtc.DataChannel) {
	var once sync.Once
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if !s.isAuthed() {
			_ = dc.Close()
			return
		}
		var req struct {
			Path string `json:"path"`
		}
		if !msg.IsString || json.Unmarshal(msg.Data, &req) != nil {
			return
		}
		once.Do(func() { go sendFile(s, dc, req.Path) })
	})
}

func sendFile(s *session, dc *webrtc.DataChannel, p string) {
	f, err := os.Open(p)
	if err != nil {
		sendJSON(dc, map[string]any{"t": "error", "message": err.Error()})
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		sendJSON(dc, map[string]any{"t": "error", "message": "not a file"})
		return
	}
	sendJSON(dc, map[string]any{"t": "meta", "name": filepath.Base(p), "size": st.Size()})

	low := make(chan struct{}, 1)
	dc.SetBufferedAmountLowThreshold(256 << 10)
	dc.OnBufferedAmountLow(func() {
		select {
		case low <- struct{}{}:
		default:
		}
	})
	buf := make([]byte, fileChunk)
	for {
		for dc.BufferedAmount() > 1<<20 {
			select {
			case <-low:
			case <-s.ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
		n, err := f.Read(buf)
		if n > 0 {
			if e := dc.Send(append([]byte(nil), buf[:n]...)); e != nil {
				return
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			sendJSON(dc, map[string]any{"t": "error", "message": err.Error()})
			return
		}
	}
	sendJSON(dc, map[string]any{"t": "end"})
	log.Printf("session %s: sent %s (%d bytes)", s.short(), p, st.Size())
}

// listDir answers the viewer's file browser.
func listDir(s *session, p string, req int) {
	if p == "" {
		p, _ = os.UserHomeDir()
	}
	p = filepath.Clean(p)
	res := map[string]any{"t": "ls", "req": req, "path": p, "parent": filepath.Dir(p), "sep": string(filepath.Separator)}
	entries, err := os.ReadDir(p)
	if err != nil {
		res["error"] = err.Error()
		s.sendCtrl(res)
		return
	}
	type ent struct {
		Name string `json:"name"`
		Dir  bool   `json:"dir"`
		Size int64  `json:"size"`
	}
	list := []ent{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, ent{e.Name(), e.IsDir(), info.Size()})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Dir != list[j].Dir {
			return list[i].Dir
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	if len(list) > 400 {
		list = list[:400]
		res["truncated"] = true
	}
	// Keep the reply under the data-channel message limit (long names add up).
	for {
		res["entries"] = list
		b, _ := json.Marshal(res)
		if len(b) <= 60000 || len(list) == 0 {
			break
		}
		list = list[:len(list)*3/4]
		res["truncated"] = true
	}
	s.sendCtrl(res)
}
