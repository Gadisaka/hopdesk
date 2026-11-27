// Command server runs the hopdesk signaling server (see internal/server).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"hopdesk/internal/server"
)

func secretFromEnv(v *string, flagName, env string) {
	if *v != "" {
		log.Printf("note: -%s on the command line is visible to other users (ps) and in shell history; prefer env %s", flagName, env)
		return
	}
	*v = os.Getenv(env)
}

func main() {
	var c server.Config
	flag.StringVar(&c.Addr, "addr", ":8080", "listen address")
	flag.StringVar(&c.AgentKey, "agent-key", "", "secret agents must present to register (better: env HOPDESK_AGENT_KEY; generated if empty)")
	flag.StringVar(&c.STUN, "stun", server.DefaultSTUN, "STUN server URL (empty to disable)")
	flag.StringVar(&c.TURN, "turn", "", "TURN server URL, e.g. turn:turn.example.com:3478 (optional)")
	flag.StringVar(&c.TURNSecret, "turn-secret", "", "coturn static-auth-secret, for short-lived TURN credentials (better: env HOPDESK_TURN_SECRET)")
	flag.StringVar(&c.TURNUser, "turn-user", "", "static TURN username (prefer -turn-secret)")
	flag.StringVar(&c.TURNPass, "turn-pass", "", "static TURN password (prefer -turn-secret)")
	flag.StringVar(&c.TLSCert, "tls-cert", "", "TLS certificate file (serve HTTPS/WSS)")
	flag.StringVar(&c.TLSKey, "tls-key", "", "TLS key file")
	flag.BoolVar(&c.BehindProxy, "behind-proxy", false, "trust X-Forwarded-For/-Host from a reverse proxy on this machine (e.g. tailscale serve, Caddy)")
	flag.StringVar(&c.AllowOrigin, "allow-origin", "", "extra viewer origins to accept, comma-separated (e.g. https://mymac.tail1234.ts.net)")
	flag.Parse()

	secretFromEnv(&c.AgentKey, "agent-key", "HOPDESK_AGENT_KEY")
	secretFromEnv(&c.TURNSecret, "turn-secret", "HOPDESK_TURN_SECRET")
	if c.AgentKey == "" {
		c.AgentKey = server.RandomKey()
		fmt.Fprintf(os.Stderr, "\nNo agent key set. Generated one for this run (set HOPDESK_AGENT_KEY to keep it):\n  %s\n\n", c.AgentKey)
	}
	if err := server.Run(context.Background(), c); err != nil {
		log.Fatal(err)
	}
}
