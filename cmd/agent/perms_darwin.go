//go:build darwin

package main

/*
#cgo LDFLAGS: -framework CoreGraphics -framework ApplicationServices -framework CoreFoundation
#include <CoreGraphics/CoreGraphics.h>
#include <ApplicationServices/ApplicationServices.h>

static int hdp_ax_ok(void) { return AXIsProcessTrusted() ? 1 : 0; }

// Shows the system "would like to control this computer" prompt.
static int hdp_ax_prompt(void) {
	const void *keys[] = { kAXTrustedCheckOptionPrompt };
	const void *vals[] = { kCFBooleanTrue };
	CFDictionaryRef opts = CFDictionaryCreate(NULL, keys, vals, 1,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	Boolean ok = AXIsProcessTrustedWithOptions(opts);
	CFRelease(opts);
	return ok ? 1 : 0;
}

static int hdp_screen_ok(void) { return CGPreflightScreenCaptureAccess() ? 1 : 0; }
static int hdp_screen_request(void) { return CGRequestScreenCaptureAccess() ? 1 : 0; }
*/
import "C"

import "os/exec"

func permSupported() bool     { return true }
func permScreen() bool        { return C.hdp_screen_ok() == 1 }
func permAccessibility() bool { return C.hdp_ax_ok() == 1 }

// requestPermission shows macOS's own prompt and opens the matching
// System Settings page, where the user switches HopDesk on.
func requestPermission(which string) {
	switch which {
	case "screen":
		if C.hdp_screen_request() == 0 {
			_ = exec.Command("open", "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture").Start()
		}
	case "accessibility":
		if C.hdp_ax_prompt() == 0 {
			_ = exec.Command("open", "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility").Start()
		}
	}
}
