//go:build windows

package main

// System sound on Windows: WASAPI loopback capture of the default speakers,
// called through COM directly (no cgo, nothing to install).

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

func audioSupported() bool { return true }

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	clsidMMDeviceEnumerator = guid{0xBCDE0395, 0xE52F, 0x467C, [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = guid{0xA95664D2, 0x9614, 0x4F35, [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioClient         = guid{0x1CB9AD4C, 0xDBFA, 0x4C32, [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidIAudioCaptureClient  = guid{0xC8ADBD64, 0xE71E, 0x48A0, [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}}
	subtypePCM              = guid{0x00000001, 0x0000, 0x0010, [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}}
	subtypeFloat            = guid{0x00000003, 0x0000, 0x0010, [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}}

	ole32                = syscall.NewLazyDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")
)

const (
	clsctxAll           = 0x17
	eRender             = 0
	eConsole            = 0
	sharedMode          = 0
	streamFlagsLoopback = 0x00020000
	bufferFlagsSilent   = 0x2
	waveFormatPCM       = 1
	waveFormatFloat     = 3
	fmtTagExtensible    = 0xFFFE
	coinitMultithreaded = 0x0
	rpcEChangedMode     = 0x80010106
	sFalse              = 1
	bufferDuration100ns = 2_000_000 // 200 ms
)

type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	Size           uint16
}

type waveFormatExtensible struct {
	waveFormatEx
	ValidBits   uint16
	ChannelMask uint32
	SubFormat   guid
}

// comCall calls method index i of a COM object's vtable.
func comCall(obj unsafe.Pointer, i int, args ...uintptr) uint32 {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, uintptr(i)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return uint32(r)
}

func comRelease(obj unsafe.Pointer) {
	if obj != nil {
		comCall(obj, 2)
	}
}

func failed(hr uint32) bool { return int32(hr) < 0 }

func hrErr(what string, hr uint32) error { return fmt.Errorf("%s failed (0x%08X)", what, hr) }

// Vtable indexes, after IUnknown's three methods.
const (
	enumGetDefaultAudioEndpoint = 4 // IMMDeviceEnumerator
	devActivate                 = 3 // IMMDevice
	acInitialize                = 3 // IAudioClient
	acGetMixFormat              = 8
	acStart                     = 10
	acStop                      = 11
	acGetService                = 14
	ccGetBuffer                 = 3 // IAudioCaptureClient
	ccReleaseBuffer             = 4
	ccGetNextPacketSize         = 5
)

// startAudioCapture captures what's playing on the default speakers until ctx ends.
func startAudioCapture(ctx context.Context, out func([]byte)) (audioFormat, error) {
	type started struct {
		f   audioFormat
		err error
	}
	ready := make(chan started, 1)
	go func() {
		// COM objects belong to the thread that made them.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		f, loop, cleanup, err := openLoopback()
		ready <- started{f, err}
		if err != nil {
			return
		}
		defer cleanup()
		loop(ctx, out)
	}()
	r := <-ready
	return r.f, r.err
}

func openLoopback() (audioFormat, func(context.Context, func([]byte)), func(), error) {
	var f audioFormat
	hr, _, _ := procCoInitializeEx.Call(0, coinitMultithreaded)
	if failed(uint32(hr)) && uint32(hr) != rpcEChangedMode {
		return f, nil, nil, hrErr("CoInitializeEx", uint32(hr))
	}
	var cleanups []func()
	if uint32(hr) == 0 || uint32(hr) == sFalse {
		cleanups = append(cleanups, func() { procCoUninitialize.Call() })
	}
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	fail := func(err error) (audioFormat, func(context.Context, func([]byte)), func(), error) {
		cleanup()
		return f, nil, nil, err
	}

	var enum unsafe.Pointer
	r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum)))
	if failed(uint32(r)) {
		return fail(hrErr("creating the device enumerator", uint32(r)))
	}
	cleanups = append(cleanups, func() { comRelease(enum) })

	var dev unsafe.Pointer
	if hr := comCall(enum, enumGetDefaultAudioEndpoint, eRender, eConsole, uintptr(unsafe.Pointer(&dev))); failed(hr) {
		return fail(errors.New("no speakers or headphones found"))
	}
	cleanups = append(cleanups, func() { comRelease(dev) })

	var client unsafe.Pointer
	if hr := comCall(dev, devActivate, uintptr(unsafe.Pointer(&iidIAudioClient)), clsctxAll, 0, uintptr(unsafe.Pointer(&client))); failed(hr) {
		return fail(hrErr("opening the audio device", hr))
	}
	cleanups = append(cleanups, func() { comRelease(client) })

	var mix *waveFormatEx
	if hr := comCall(client, acGetMixFormat, uintptr(unsafe.Pointer(&mix))); failed(hr) || mix == nil {
		return fail(hrErr("reading the audio format", hr))
	}
	cleanups = append(cleanups, func() { procCoTaskMemFree.Call(uintptr(unsafe.Pointer(mix))) })

	f.Rate, f.Channels = int(mix.SamplesPerSec), int(mix.Channels)
	isFloat := mix.FormatTag == waveFormatFloat
	isPCM := mix.FormatTag == waveFormatPCM
	if mix.FormatTag == fmtTagExtensible && mix.Size >= 22 {
		ext := (*waveFormatExtensible)(unsafe.Pointer(mix))
		isFloat, isPCM = ext.SubFormat == subtypeFloat, ext.SubFormat == subtypePCM
	}
	switch {
	case isFloat && mix.BitsPerSample == 32:
		f.Sample = "f32le"
	case isPCM && mix.BitsPerSample == 16:
		f.Sample = "s16le"
	case isPCM && mix.BitsPerSample == 32:
		f.Sample = "s32le"
	default:
		return fail(fmt.Errorf("unsupported audio format (tag 0x%X, %d bits)", mix.FormatTag, mix.BitsPerSample))
	}
	if f.Channels < 1 || f.Channels > 8 || f.Rate < 8000 {
		return fail(fmt.Errorf("unsupported audio format (%d Hz, %d channels)", f.Rate, f.Channels))
	}

	if hr := comCall(client, acInitialize, sharedMode, streamFlagsLoopback, bufferDuration100ns, 0, uintptr(unsafe.Pointer(mix)), 0); failed(hr) {
		return fail(hrErr("starting loopback capture", hr))
	}
	var capture unsafe.Pointer
	if hr := comCall(client, acGetService, uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&capture))); failed(hr) {
		return fail(hrErr("getting the capture client", hr))
	}
	cleanups = append(cleanups, func() { comRelease(capture) })
	if hr := comCall(client, acStart); failed(hr) {
		return fail(hrErr("starting capture", hr))
	}
	cleanups = append(cleanups, func() { comCall(client, acStop) })

	frameBytes := int(mix.BlockAlign)
	loop := func(ctx context.Context, out func([]byte)) {
		t := time.NewTicker(10 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			for {
				var packet uint32
				if hr := comCall(capture, ccGetNextPacketSize, uintptr(unsafe.Pointer(&packet))); failed(hr) || packet == 0 {
					break
				}
				var data *byte
				var frames, flags uint32
				if hr := comCall(capture, ccGetBuffer, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)),
					uintptr(unsafe.Pointer(&flags)), 0, 0); failed(hr) {
					break
				}
				n := int(frames) * frameBytes
				buf := make([]byte, n)
				if flags&bufferFlagsSilent == 0 && data != nil && n > 0 {
					copy(buf, unsafe.Slice(data, n))
				}
				comCall(capture, ccReleaseBuffer, uintptr(frames))
				if n > 0 {
					out(buf)
				}
			}
		}
	}
	return f, loop, cleanup, nil
}
