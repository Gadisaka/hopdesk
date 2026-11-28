package main

// Bundled Tailscale for "this computer is the server": HopDesk runs its own
// Tailscale node (tsnet, no separate install) and publishes the server with
// Tailscale Funnel, so your other computers and any browser can reach it at
// https://hopdesk-<name>.<tailnet>.ts.net without installing anything.
//
//   :443  (Funnel) the viewer page and signaling
//   :8443 (Funnel) a TURN relay over TLS, for when two devices can't connect
//                  directly (e.g. both behind carrier-grade NAT)

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"log"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/turn/v4"
	"tailscale.com/tsnet"

	"hopdesk/internal/server"
)

// tailnetState is what the setup page shows.
type tailnetState struct {
	State   string `json:"state"` // off, starting, needs-login, running, error
	AuthURL string `json:"authUrl,omitempty"`
	DNSName string `json:"dnsName,omitempty"`
	Public  bool   `json:"public"`            // Funnel is up: reachable from the internet
	Problem string `json:"problem,omitempty"` // e.g. HTTPS or Funnel not enabled yet
}

var tailnet struct {
	sync.Mutex
	st     tailnetState
	logout func() error
}

func getTailnet() tailnetState {
	tailnet.Lock()
	defer tailnet.Unlock()
	if tailnet.st.State == "" {
		return tailnetState{State: "off"}
	}
	return tailnet.st
}

func updateTailnet(f func(*tailnetState)) {
	tailnet.Lock()
	f(&tailnet.st)
	tailnet.Unlock()
}

func tailnetLogout() error {
	tailnet.Lock()
	fn := tailnet.logout
	tailnet.Unlock()
	if fn == nil {
		return errUnsupported
	}
	return fn()
}

var nonLabel = regexp.MustCompile(`[^a-z0-9-]+`)

func tailnetHostname(id string) string {
	h := "hopdesk-" + strings.Trim(nonLabel.ReplaceAllString(strings.ToLower(id), "-"), "-")
	if len(h) > 63 {
		h = h[:63]
	}
	return strings.TrimRight(h, "-")
}

// startTailnet joins Tailscale and publishes srv through Funnel. It runs for
// the life of the app and keeps retrying the steps the user still has to do.
func startTailnet(srv *server.Server) {
	updateTailnet(func(s *tailnetState) { *s = tailnetState{State: "starting"} })
	ts := &tsnet.Server{
		Dir:      filepath.Join(appDir(), "tailscale"),
		Hostname: tailnetHostname(*agentID),
		Logf:     func(string, ...any) {}, // very chatty; keep the HopDesk log readable
		UserLogf: func(format string, args ...any) { log.Printf("tailscale: "+format, args...) },
	}
	fail := func(msg string) {
		log.Printf("tailscale: %s", msg)
		updateTailnet(func(s *tailnetState) { s.State, s.Problem = "error", msg })
	}
	if err := ts.Start(); err != nil {
		fail(err.Error())
		return
	}
	lc, err := ts.LocalClient()
	if err != nil {
		fail(err.Error())
		return
	}
	tailnet.Lock()
	tailnet.logout = func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return lc.Logout(ctx)
	}
	tailnet.Unlock()

	// 1. Sign in (once). The setup page shows the link.
	ctx := context.Background()
	dns := ""
	askedLogin := false
	for dns == "" {
		st, err := lc.StatusWithoutPeers(ctx)
		if err == nil {
			switch st.BackendState {
			case "Running":
				if st.Self != nil {
					dns = strings.TrimSuffix(st.Self.DNSName, ".")
				}
			case "NeedsLogin":
				if st.AuthURL == "" && !askedLogin {
					askedLogin = true
					_ = lc.StartLoginInteractive(ctx)
				}
				url := st.AuthURL
				updateTailnet(func(s *tailnetState) { s.State, s.AuthURL = "needs-login", url })
			case "NeedsMachineAuth":
				updateTailnet(func(s *tailnetState) {
					s.State, s.Problem = "error", "Approve this device in the Tailscale admin console (Machines)."
				})
			}
		}
		if dns == "" {
			time.Sleep(2 * time.Second)
		}
	}
	log.Printf("tailscale: online as %s", dns)
	updateTailnet(func(s *tailnetState) {
		s.State, s.AuthURL, s.DNSName, s.Problem = "running", "", dns, ""
	})

	// 2. Publish with Funnel. This fails until HTTPS and Funnel are allowed in
	//    the admin console, so retry while the user switches them on.
	var web, relay net.Listener
	for {
		if web == nil {
			web, err = ts.ListenFunnel("tcp", ":443")
		}
		if err == nil && relay == nil {
			relay, err = ts.ListenFunnel("tcp", ":8443")
		}
		if err == nil {
			break
		}
		msg := err.Error()
		log.Printf("tailscale: funnel: %s", msg)
		updateTailnet(func(s *tailnetState) { s.Public, s.Problem = false, funnelHelp(msg) })
		time.Sleep(20 * time.Second)
	}
	go func() {
		if err := srv.Serve(web); err != nil {
			fail("viewer page: " + err.Error())
		}
	}()

	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	if err := startRelay(relay, hex.EncodeToString(secret)); err != nil {
		log.Printf("relay: %v", err) // direct connections still work
	} else {
		srv.SetTURN("turns:"+dns+":8443?transport=tcp", hex.EncodeToString(secret))
	}
	log.Printf("tailscale: published at https://%s", dns)
	updateTailnet(func(s *tailnetState) { s.Public, s.Problem = true, "" })
}

func funnelHelp(msg string) string {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "https"), strings.Contains(low, "cert"):
		return "Turn on HTTPS certificates in the Tailscale admin console (DNS page): https://login.tailscale.com/admin/dns"
	case strings.Contains(low, "funnel"):
		if u := regexp.MustCompile(`https://\S+`).FindString(msg); u != "" {
			return "Allow Funnel for this device: " + strings.TrimRight(u, ".,)")
		}
		return "Allow Funnel in the Tailscale admin console (Access controls → add the \"funnel\" node attribute): https://login.tailscale.com/admin/acls/file"
	}
	return msg
}

// startRelay runs a TURN server on ln (TURN over TLS through Funnel). Relay
// sockets live on this computer, so two relayed devices meet here.
func startRelay(ln net.Listener, secret string) error {
	_, err := turn.NewServer(turn.ServerConfig{
		Realm: "hopdesk",
		// Credentials are "<expiry>:hopdesk" / HMAC-SHA1(secret, username), as
		// issued by the server's /config, and expire after an hour.
		AuthHandler: func(username, realm string, _ net.Addr) ([]byte, bool) {
			exp, _, ok := strings.Cut(username, ":")
			if !ok {
				return nil, false
			}
			t, err := strconv.ParseInt(exp, 10, 64)
			if err != nil || time.Now().Unix() > t {
				return nil, false
			}
			m := hmac.New(sha1.New, []byte(secret))
			m.Write([]byte(username))
			return turn.GenerateAuthKey(username, realm, base64.StdEncoding.EncodeToString(m.Sum(nil))), true
		},
		ListenerConfigs: []turn.ListenerConfig{{
			Listener:              ln,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: relayIP(), Address: relayIP().String()},
		}},
	})
	return err
}

// relayIP is this computer's LAN address (loopback as a last resort).
func relayIP() net.IP {
	c, err := net.Dial("udp", "192.0.2.1:9") // no packets are sent; this just picks the outgoing interface
	if err == nil {
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.IP.To4() != nil {
			return a.IP.To4()
		}
	}
	return net.IPv4(127, 0, 0, 1)
}
