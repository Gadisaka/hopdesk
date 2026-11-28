package main

import (
	_ "embed"
	"fmt"
	"os"
	"runtime"
	"time"

	"fyne.io/systray"
	"github.com/atotto/clipboard"
)

//go:embed assets/tray_mac.png
var trayMacIcon []byte

//go:embed assets/tray.ico
var trayWinIcon []byte

//go:embed assets/tray.png
var trayPNGIcon []byte

// The macOS menu bar must be driven from the main thread.
func init() { runtime.LockOSThread() }

func quitApp() {
	systray.Quit()
	time.Sleep(time.Second)
	os.Exit(0)
}

// runTray shows the menu-bar (macOS) or notification-area (Windows) icon. It blocks.
func runTray(ui *uiServer) {
	systray.Run(func() {
		switch runtime.GOOS {
		case "darwin":
			systray.SetTemplateIcon(trayMacIcon, trayMacIcon)
		case "windows":
			systray.SetIcon(trayWinIcon)
		default:
			systray.SetIcon(trayPNGIcon)
		}
		systray.SetTooltip("HopDesk")
		mStatus := systray.AddMenuItem("Starting…", "")
		mStatus.Disable()
		mOpen := systray.AddMenuItem("Open HopDesk…", "Settings, password and permissions")
		mCopy := systray.AddMenuItem("Copy device name", "Copy this computer's ID")
		mUpdate := systray.AddMenuItem("Update available…", "Open HopDesk to install the new version")
		mUpdate.Hide()
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Quit HopDesk", "Stop accepting connections")

		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			last, lastUpd := "", ""
			for {
				text := trayStatus()
				if text != last {
					mStatus.SetTitle(text)
					systray.SetTooltip("HopDesk: " + text)
					last = text
				}
				if u := getUpdateState(); u.State == "available" && u.Latest != lastUpd {
					mUpdate.SetTitle("Install HopDesk " + u.Latest + "…")
					mUpdate.Show()
					lastUpd = u.Latest
				} else if u.State != "available" && lastUpd != "" && u.State != "downloading" && u.State != "installing" {
					mUpdate.Hide()
					lastUpd = ""
				}
				select {
				case <-t.C:
				case <-mOpen.ClickedCh:
					openURL(ui.url())
				case <-mUpdate.ClickedCh:
					openURL(ui.url())
				case <-mCopy.ClickedCh:
					appMu.Lock()
					id := appCfg.DeviceID
					appMu.Unlock()
					_ = clipboard.WriteAll(id)
				case <-mQuit.ClickedCh:
					quitApp()
				}
			}
		}()
	}, func() {})
}

func trayStatus() string {
	state, _ := getStatus()
	appMu.Lock()
	id := appCfg.DeviceID
	appMu.Unlock()
	switch state {
	case "online":
		if n := activeSessions(); n > 0 {
			return fmt.Sprintf("● %d viewer(s) connected", n)
		}
		return "● Online as " + id
	case "connecting":
		return "○ Connecting…"
	case "setup":
		return "○ Setup needed"
	case "offline":
		return "○ Offline, retrying"
	case "error":
		return "⚠ Problem, open HopDesk"
	}
	return "○ Starting…"
}
