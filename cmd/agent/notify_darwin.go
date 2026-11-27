//go:build darwin

package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Text is passed as script arguments, never pasted into the script, so it
// can't inject AppleScript.

func notify(title, msg string) {
	_ = exec.Command("osascript",
		"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run",
		title, msg).Start()
}

// askApproval shows a dialog on this Mac; no answer within 30 s means no.
func askApproval(msg string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "osascript",
		"-e", "on run argv",
		"-e", `display dialog (item 1 of argv) with title "hopdesk" buttons {"Deny", "Allow"} default button "Deny" cancel button "Deny" with icon caution giving up after 30`,
		"-e", "end run", msg).Output()
	if err != nil {
		return false
	}
	s := string(out)
	return strings.Contains(s, "button returned:Allow") && !strings.Contains(s, "gave up:true")
}
