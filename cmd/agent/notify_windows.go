//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Text is passed through environment variables, never pasted into the
// script, so it can't inject PowerShell.

func powershell(ctx context.Context, script string, env ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

func notify(title, msg string) {
	const script = `Add-Type -AssemblyName System.Windows.Forms;` +
		`$n = New-Object System.Windows.Forms.NotifyIcon;` +
		`$n.Icon = [System.Drawing.SystemIcons]::Information; $n.Visible = $true;` +
		`$n.ShowBalloonTip(8000, $env:HOPDESK_TITLE, $env:HOPDESK_MSG, 'Info'); Start-Sleep -Seconds 9; $n.Dispose()`
	_ = powershell(context.Background(), script, "HOPDESK_TITLE="+title, "HOPDESK_MSG="+msg).Start()
}

// askApproval shows a Yes/No box on this PC; no answer within 30 s means no.
func askApproval(msg string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	// 4 = Yes/No, 48 = warning icon, 256 = "No" is the default button, 4096 = always on top
	const script = `(New-Object -ComObject WScript.Shell).Popup($env:HOPDESK_MSG, 30, 'hopdesk', 4 + 48 + 256 + 4096)`
	out, err := powershell(ctx, script, "HOPDESK_MSG="+msg).Output()
	return err == nil && strings.TrimSpace(string(out)) == "6" // 6 = Yes
}
