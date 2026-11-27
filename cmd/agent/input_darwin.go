//go:build darwin

package main

/*
#cgo LDFLAGS: -framework CoreGraphics -framework ApplicationServices
#include <CoreGraphics/CoreGraphics.h>
#include <ApplicationServices/ApplicationServices.h>

static void hd_mouse(int type, double x, double y, int button, int clicks) {
	CGEventRef e = CGEventCreateMouseEvent(NULL, (CGEventType)type, CGPointMake(x, y), (CGMouseButton)button);
	if (!e) return;
	if (clicks > 0) CGEventSetIntegerValueField(e, kCGMouseEventClickState, clicks);
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
}

static void hd_scroll(int dx, int dy) {
	CGEventRef e = CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitPixel, 2, dy, dx);
	if (!e) return;
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
}

static void hd_key(int keycode, int down, unsigned long long flags) {
	CGEventRef e = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)keycode, down ? true : false);
	if (!e) return;
	CGEventSetFlags(e, (CGEventFlags)flags);
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
}

static void hd_unicode(unsigned short ch) {
	UniChar c = ch;
	CGEventRef d = CGEventCreateKeyboardEvent(NULL, 0, true);
	CGEventRef u = CGEventCreateKeyboardEvent(NULL, 0, false);
	if (!d || !u) { if (d) CFRelease(d); if (u) CFRelease(u); return; }
	CGEventKeyboardSetUnicodeString(d, 1, &c);
	CGEventKeyboardSetUnicodeString(u, 1, &c);
	CGEventPost(kCGHIDEventTap, d);
	CGEventPost(kCGHIDEventTap, u);
	CFRelease(d);
	CFRelease(u);
}

static int hd_trusted(void) { return AXIsProcessTrusted() ? 1 : 0; }
*/
import "C"

import (
	"errors"
	"sync"
	"unicode/utf16"
)

// CGEventType values
const (
	evLeftDown     = 1
	evLeftUp       = 2
	evRightDown    = 3
	evRightUp      = 4
	evMoved        = 5
	evLeftDragged  = 6
	evRightDragged = 7
	evOtherDown    = 25
	evOtherUp      = 26
	evOtherDragged = 27
)

// CGEventFlags
const (
	flagShift   = 0x20000
	flagControl = 0x40000
	flagOption  = 0x80000
	flagCommand = 0x100000
)

var macKeys = map[string]int{
	"KeyA": 0x00, "KeyS": 0x01, "KeyD": 0x02, "KeyF": 0x03, "KeyH": 0x04, "KeyG": 0x05,
	"KeyZ": 0x06, "KeyX": 0x07, "KeyC": 0x08, "KeyV": 0x09, "KeyB": 0x0B, "KeyQ": 0x0C,
	"KeyW": 0x0D, "KeyE": 0x0E, "KeyR": 0x0F, "KeyY": 0x10, "KeyT": 0x11, "KeyO": 0x1F,
	"KeyU": 0x20, "KeyI": 0x22, "KeyP": 0x23, "KeyL": 0x25, "KeyJ": 0x26, "KeyK": 0x28,
	"KeyN": 0x2D, "KeyM": 0x2E,
	"Digit1": 0x12, "Digit2": 0x13, "Digit3": 0x14, "Digit4": 0x15, "Digit5": 0x17,
	"Digit6": 0x16, "Digit7": 0x1A, "Digit8": 0x1C, "Digit9": 0x19, "Digit0": 0x1D,
	"Equal": 0x18, "Minus": 0x1B, "BracketRight": 0x1E, "BracketLeft": 0x21, "Quote": 0x27,
	"Semicolon": 0x29, "Backslash": 0x2A, "Comma": 0x2B, "Slash": 0x2C, "Period": 0x2F,
	"Backquote": 0x32, "IntlBackslash": 0x0A,
	"Enter": 0x24, "NumpadEnter": 0x4C, "Tab": 0x30, "Space": 0x31, "Backspace": 0x33,
	"Escape": 0x35, "CapsLock": 0x39, "Delete": 0x75, "Home": 0x73, "End": 0x77,
	"PageUp": 0x74, "PageDown": 0x79,
	"ArrowLeft": 0x7B, "ArrowRight": 0x7C, "ArrowDown": 0x7D, "ArrowUp": 0x7E,
	"MetaLeft": 0x37, "MetaRight": 0x36, "ShiftLeft": 0x38, "ShiftRight": 0x3C,
	"AltLeft": 0x3A, "AltRight": 0x3D, "ControlLeft": 0x3B, "ControlRight": 0x3E,
	"F1": 0x7A, "F2": 0x78, "F3": 0x63, "F4": 0x76, "F5": 0x60, "F6": 0x61,
	"F7": 0x62, "F8": 0x64, "F9": 0x65, "F10": 0x6D, "F11": 0x67, "F12": 0x6F,
}

var modFlags = map[string]uint64{
	"ShiftLeft": flagShift, "ShiftRight": flagShift,
	"ControlLeft": flagControl, "ControlRight": flagControl,
	"AltLeft": flagOption, "AltRight": flagOption,
	"MetaLeft": flagCommand, "MetaRight": flagCommand,
}

var st struct {
	sync.Mutex
	buttons map[int]bool
	mods    map[string]bool
}

func platformInit() error {
	st.buttons = map[int]bool{}
	st.mods = map[string]bool{}
	if C.hd_trusted() == 0 {
		return errors.New("Accessibility permission is off, so mouse and keyboard control won't work. " +
			"Turn it on in System Settings > Privacy & Security > Accessibility for the app running the agent (e.g. Terminal), then restart the agent")
	}
	return nil
}

func currentFlags() uint64 {
	var f uint64
	for code, down := range st.mods {
		if down {
			f |= modFlags[code]
		}
	}
	return f
}

func inputMove(x, y float64) {
	st.Lock()
	defer st.Unlock()
	t := evMoved
	btn := 0
	switch {
	case st.buttons[0]:
		t = evLeftDragged
	case st.buttons[2]:
		t, btn = evRightDragged, 1
	case st.buttons[1]:
		t, btn = evOtherDragged, 2
	}
	C.hd_mouse(C.int(t), C.double(x), C.double(y), C.int(btn), 0)
}

func inputButton(x, y float64, b int, down bool, clicks int) {
	st.Lock()
	defer st.Unlock()
	st.buttons[b] = down
	var t, btn int
	switch b {
	case 0:
		t = evLeftUp
		if down {
			t = evLeftDown
		}
	case 2:
		t, btn = evRightUp, 1
		if down {
			t = evRightDown
		}
	default:
		t, btn = evOtherUp, 2
		if down {
			t = evOtherDown
		}
	}
	C.hd_mouse(C.int(t), C.double(x), C.double(y), C.int(btn), C.int(clicks))
}

func inputScroll(dx, dy float64) {
	// Browser: positive deltaY = scroll down. CoreGraphics: positive = scroll up.
	C.hd_scroll(C.int(-dx), C.int(-dy))
}

func inputKey(code, key string, down bool) {
	st.Lock()
	defer st.Unlock()
	if _, isMod := modFlags[code]; isMod {
		st.mods[code] = down
	}
	if kc, ok := macKeys[code]; ok {
		var d C.int
		if down {
			d = 1
		}
		C.hd_key(C.int(kc), d, C.ulonglong(currentFlags()))
		return
	}
	// Unknown physical key: type the character itself.
	if down && len([]rune(key)) == 1 {
		for _, u := range utf16.Encode([]rune(key)) {
			C.hd_unicode(C.ushort(u))
		}
	}
}
