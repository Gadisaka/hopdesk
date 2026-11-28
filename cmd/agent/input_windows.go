//go:build windows

package main

import (
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	user32                         = syscall.NewLazyDLL("user32.dll")
	procSendInput                  = user32.NewProc("SendInput")
	procSetCursorPos               = user32.NewProc("SetCursorPos")
	procMapVirtualKeyW             = user32.NewProc("MapVirtualKeyW")
	procSetProcessDPIAware         = user32.NewProc("SetProcessDPIAware")
	procSetProcessDpiAwarenessCtxt = user32.NewProc("SetProcessDpiAwarenessContext")
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseLeftDown   = 0x0002
	mouseLeftUp     = 0x0004
	mouseRightDown  = 0x0008
	mouseRightUp    = 0x0010
	mouseMiddleDown = 0x0020
	mouseMiddleUp   = 0x0040
	mouseWheel      = 0x0800
	mouseHWheel     = 0x1000

	keyExtended = 0x0001
	keyUp       = 0x0002
	keyUnicode  = 0x0004
)

// Go lays these out exactly like the Win32 structs on both 32- and 64-bit.
type mouseInput struct {
	dx, dy    int32
	mouseData uint32
	flags     uint32
	time      uint32
	extra     uintptr
}

type keybdInput struct {
	vk, scan uint16
	flags    uint32
	time     uint32
	extra    uintptr
}

type mInput struct {
	typ uint32
	mi  mouseInput
}

type kInput struct {
	typ uint32
	ki  keybdInput
	_   [8]byte // pad to sizeof(INPUT), whose union is sized by MOUSEINPUT
}

func init() {
	// Make coordinates physical pixels so they match the captured screen.
	if procSetProcessDpiAwarenessCtxt.Find() == nil {
		r, _, _ := procSetProcessDpiAwarenessCtxt.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2 (-4)
		if r != 0 {
			return
		}
	}
	if procSetProcessDPIAware.Find() == nil {
		procSetProcessDPIAware.Call()
	}
}

var winKeys = map[string]uint16{
	"Enter": 0x0D, "NumpadEnter": 0x0D, "Escape": 0x1B, "Backspace": 0x08, "Tab": 0x09,
	"Space": 0x20, "CapsLock": 0x14, "Delete": 0x2E, "Insert": 0x2D, "Home": 0x24,
	"End": 0x23, "PageUp": 0x21, "PageDown": 0x22,
	"ArrowLeft": 0x25, "ArrowUp": 0x26, "ArrowRight": 0x27, "ArrowDown": 0x28,
	"ShiftLeft": 0xA0, "ShiftRight": 0xA1, "ControlLeft": 0xA2, "ControlRight": 0xA3,
	"AltLeft": 0xA4, "AltRight": 0xA5, "MetaLeft": 0x5B, "MetaRight": 0x5C,
	"Minus": 0xBD, "Equal": 0xBB, "BracketLeft": 0xDB, "BracketRight": 0xDD,
	"Backslash": 0xDC, "Semicolon": 0xBA, "Quote": 0xDE, "Comma": 0xBC,
	"Period": 0xBE, "Slash": 0xBF, "Backquote": 0xC0, "IntlBackslash": 0xE2,
	"PrintScreen": 0x2C, "ContextMenu": 0x5D,
}

var extendedKeys = map[string]bool{
	"ArrowLeft": true, "ArrowUp": true, "ArrowRight": true, "ArrowDown": true,
	"Home": true, "End": true, "PageUp": true, "PageDown": true, "Insert": true,
	"Delete": true, "ControlRight": true, "AltRight": true, "MetaLeft": true,
	"MetaRight": true, "NumpadEnter": true, "ContextMenu": true,
}

func init() {
	for c := 'A'; c <= 'Z'; c++ {
		winKeys["Key"+string(c)] = uint16(c)
	}
	for c := '0'; c <= '9'; c++ {
		winKeys["Digit"+string(c)] = uint16(c)
	}
	for i := 1; i <= 12; i++ {
		winKeys["F"+itoa(i)] = uint16(0x70 + i - 1)
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

var mu sync.Mutex

// The exported input functions run on the thread that follows the desktop
// that's showing (see desktop_windows.go), so clicks and typing also reach
// the login screen and admin prompts when HopDesk runs as a service.

func platformInit() error { return nil }

func sendMouse(flags, data uint32) {
	in := mInput{typ: inputMouse, mi: mouseInput{mouseData: data, flags: flags}}
	procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
}

func sendKey(vk, scan uint16, flags uint32) {
	in := kInput{typ: inputKeyboard, ki: keybdInput{vk: vk, scan: scan, flags: flags}}
	procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
}

func setCursor(x, y float64) {
	procSetCursorPos.Call(uintptr(int32(x)), uintptr(int32(y)))
}

func inputMoveNow(x, y float64) {
	mu.Lock()
	defer mu.Unlock()
	setCursor(x, y)
}

func inputButtonNow(x, y float64, b int, down bool, _ int) {
	mu.Lock()
	defer mu.Unlock()
	setCursor(x, y)
	var f uint32
	switch b {
	case 0:
		f = mouseLeftUp
		if down {
			f = mouseLeftDown
		}
	case 2:
		f = mouseRightUp
		if down {
			f = mouseRightDown
		}
	default:
		f = mouseMiddleUp
		if down {
			f = mouseMiddleDown
		}
	}
	sendMouse(f, 0)
}

func inputScrollNow(dx, dy float64) {
	mu.Lock()
	defer mu.Unlock()
	// Browser: positive deltaY = down. Windows: positive wheel = up.
	if dy != 0 {
		sendMouse(mouseWheel, uint32(int32(-dy)))
	}
	if dx != 0 {
		sendMouse(mouseHWheel, uint32(int32(dx)))
	}
}

func inputKeyNow(code, key string, down bool) {
	mu.Lock()
	defer mu.Unlock()
	if vk, ok := winKeys[code]; ok {
		scan, _, _ := procMapVirtualKeyW.Call(uintptr(vk), 0)
		var f uint32
		if extendedKeys[code] {
			f |= keyExtended
		}
		if !down {
			f |= keyUp
		}
		sendKey(vk, uint16(scan), f)
		return
	}
	if down && len([]rune(key)) == 1 {
		for _, u := range utf16.Encode([]rune(key)) {
			sendKey(0, u, keyUnicode)
			sendKey(0, u, keyUnicode|keyUp)
		}
	}
}

func inputMove(x, y float64) { onInputDesktop(func() { inputMoveNow(x, y) }) }

func inputButton(x, y float64, b int, down bool, clicks int) {
	onInputDesktop(func() { inputButtonNow(x, y, b, down, clicks) })
}

func inputScroll(dx, dy float64) { onInputDesktop(func() { inputScrollNow(dx, dy) }) }

func inputKey(code, key string, down bool) { onInputDesktop(func() { inputKeyNow(code, key, down) }) }
