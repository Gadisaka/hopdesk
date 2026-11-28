package main

import (
	"log"
	"sync"
)

var loggedOnce sync.Map

// logOnce logs a message the first time a key is seen, to keep repeating
// conditions (checked many times a second) from flooding the log.
func logOnce(key, format string, args ...any) {
	if _, seen := loggedOnce.LoadOrStore(key, true); !seen {
		log.Printf(format, args...)
	}
}
