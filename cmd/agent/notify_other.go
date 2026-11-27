//go:build !darwin && !windows

package main

import "log"

func notify(title, msg string) { log.Printf("%s: %s", title, msg) }

// askApproval has no desktop dialog on this platform, so it refuses.
func askApproval(msg string) bool {
	log.Printf("-confirm is not supported on this platform; refusing: %s", msg)
	return false
}
