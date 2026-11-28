//go:build windows

package main

// Windows shows the login screen, the lock screen and admin (UAC) prompts on
// a separate, protected desktop. A program only sees and controls the desktop
// its thread is attached to, so screen capture and input run on one dedicated
// thread that follows whichever desktop is currently showing. Only the
// HopDesk service's worker (running as the system account) is allowed to
// attach to the protected desktop; a normal HopDesk just stays on its own.

import (
	"image"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/kbinani/screenshot"
	"golang.org/x/sys/windows"
)

var (
	procOpenInputDesktop         = user32.NewProc("OpenInputDesktop")
	procSetThreadDesktop         = user32.NewProc("SetThreadDesktop")
	procCloseDesktop             = user32.NewProc("CloseDesktop")
	procGetUserObjectInformation = user32.NewProc("GetUserObjectInformationW")
)

const (
	uoiName           = 2
	desktopAllAccess  = 0x01FF // DESKTOP_* rights: read, write, switch, hooks, journal
	desktopCheckEvery = 250 * time.Millisecond
)

var desk struct {
	once    sync.Once
	work    chan func()
	cur     uintptr // desktop handle the thread is attached to (0 = the one it started on)
	name    string
	checked time.Time
}

// onInputDesktop runs f on the thread attached to the desktop that is
// currently showing, and waits for it to finish.
func onInputDesktop(f func()) {
	desk.once.Do(func() {
		desk.work = make(chan func())
		go deskThread()
	})
	done := make(chan struct{})
	desk.work <- func() { f(); close(done) }
	<-done
}

func deskThread() {
	runtime.LockOSThread() // this goroutine owns the thread for good
	for f := range desk.work {
		if time.Since(desk.checked) > desktopCheckEvery {
			followInputDesktop()
			desk.checked = time.Now()
		}
		f()
	}
}

// captureRect grabs part of the screen that's showing. If that fails (the
// desktop may have just switched), it checks the desktop and tries once more.
func captureRect(r image.Rectangle) (img *image.RGBA, err error) {
	onInputDesktop(func() {
		img, err = screenshot.CaptureRect(r)
		if err != nil {
			followInputDesktop()
			desk.checked = time.Now()
			img, err = screenshot.CaptureRect(r)
		}
	})
	return img, err
}

// followInputDesktop attaches the thread to the input desktop if it changed.
func followInputDesktop() {
	h, _, _ := procOpenInputDesktop.Call(0, 0, desktopAllAccess)
	if h == 0 {
		return // not allowed (normal app) or no desktop right now: stay where we are
	}
	name := desktopName(h)
	if name == desk.name && desk.cur != 0 {
		procCloseDesktop.Call(h)
		return
	}
	if ok, _, err := procSetThreadDesktop.Call(h); ok == 0 {
		logOnce("desktop", "can't switch to the %q desktop: %v", name, err)
		procCloseDesktop.Call(h)
		return
	}
	if desk.cur != 0 {
		procCloseDesktop.Call(desk.cur)
	}
	if desk.name != "" {
		logOnce("desktop:"+name, "now showing the %q desktop", name)
	}
	desk.cur, desk.name = h, name
}

func desktopName(h uintptr) string {
	var buf [128]uint16
	var n uint32
	ok, _, _ := procGetUserObjectInformation.Call(h, uoiName, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&n)))
	if ok == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:])
}
