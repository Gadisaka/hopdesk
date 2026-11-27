package main

import (
	"log"
	"sync"
	"time"
)

// guard limits password guessing.
//
//   - Per source address: 5 wrong passwords lock that address for 15 min,
//     doubling each time it happens again (up to 24 h). Other addresses,
//     including yours, are unaffected.
//   - Overall: at most 30 wrong passwords per hour from all addresses
//     together, which caps attackers who rotate addresses.
const (
	perIPFails   = 5
	baseLockout  = 15 * time.Minute
	maxLockout   = 24 * time.Hour
	globalFails  = 30
	globalWindow = time.Hour
)

type ipFail struct {
	fails   int
	strikes int
	until   time.Time
}

type guardT struct {
	mu     sync.Mutex
	ips    map[string]*ipFail
	recent []time.Time
}

var guard = &guardT{ips: map[string]*ipFail{}}

// check reports whether a password attempt from ip may proceed.
func (g *guardT) check(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if f := g.ips[ip]; f != nil && time.Now().Before(f.until) {
		return false
	}
	g.prune()
	return len(g.recent) < globalFails
}

func (g *guardT) prune() {
	cut := time.Now().Add(-globalWindow)
	i := 0
	for i < len(g.recent) && g.recent[i].Before(cut) {
		i++
	}
	g.recent = g.recent[i:]
}

func (g *guardT) fail(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.recent = append(g.recent, time.Now())
	f := g.ips[ip]
	if f == nil {
		f = &ipFail{}
		g.ips[ip] = f
	}
	f.fails++
	if f.fails >= perIPFails {
		f.fails = 0
		f.strikes++
		lock := baseLockout << (f.strikes - 1)
		if lock > maxLockout || lock <= 0 {
			lock = maxLockout
		}
		f.until = time.Now().Add(lock)
		log.Printf("security: %d wrong passwords from %s; blocking it for %s", perIPFails, ip, lock)
	}
	if len(g.recent) == globalFails {
		log.Printf("security: %d wrong passwords in the last hour; pausing password checks", globalFails)
	}
}

func (g *guardT) success(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if f := g.ips[ip]; f != nil && time.Now().After(f.until) {
		delete(g.ips, ip)
	}
}

// ---- session caps ----

const (
	maxPending  = 3 // connections that haven't proved the password yet
	maxSessions = 4 // all connections
)

var capMu sync.Mutex
var pendingN, sessionN int

func reserveSession() bool {
	capMu.Lock()
	defer capMu.Unlock()
	if pendingN >= maxPending || sessionN >= maxSessions {
		return false
	}
	pendingN++
	sessionN++
	return true
}

func promoteSession() { capMu.Lock(); pendingN--; capMu.Unlock() }

func releaseSession(wasPending bool) {
	capMu.Lock()
	defer capMu.Unlock()
	sessionN--
	if wasPending {
		pendingN--
	}
}
