package main

import (
	"strings"
	"testing"
)

// Tailscale errors end with a https:// link; that must not be read as "HTTPS is off".
func TestFunnelHelp(t *testing.T) {
	cases := map[string]string{
		`Funnel not available; "funnel" node attribute not set. See https://tailscale.com/s/no-funnel.`: "nodeAttrs",
		`Funnel not available; HTTPS must be enabled. See https://tailscale.com/s/https.`:               "HTTPS certificates",
		`tsnet: you must enable HTTPS in the admin panel to proceed. See https://tailscale.com/s/https`: "HTTPS certificates",
	}
	for in, want := range cases {
		if got := funnelHelp(in); !strings.Contains(got, want) {
			t.Errorf("%q -> %q", in, got)
		}
	}
}
