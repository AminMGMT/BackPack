package core

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
)

// A tunnel v2.0.0 left unable to start is still listed — so the menus and the
// panel show it, and Delete can reach it — with the engine's reason attached,
// rather than vanishing while its service is still installed. One that can
// start carries no reason at all.
func TestTunnelsThatCanNoLongerRunAreListedWithTheReason(t *testing.T) {
	for _, tc := range []struct {
		name, body, transport, role, reason string
	}{
		{"stream", `[direct]
role = "kharej"
addr = "0.0.0.0:8443"
transport = "wss"
`, "direct/wss", "kharej", "[direct] stream tunnel"},
		{"udp", `[l3]
mode = "dial"
addr = "203.0.113.9:9000"
carrier = "udp"
`, "l3/udp", "iran", "removed in Backpack v2.0.0"},
		{"quic", `[l3]
mode = "listen"
addr = "0.0.0.0:9000"
carrier = "quic"
`, "l3/quic", "kharej", "removed in Backpack v2.0.0"},
		{"no-carrier", `[l3]
mode = "dial"
addr = "203.0.113.9:9000"
`, "l3/udp", "iran", "which meant udp"},
		{"pck", `[l3]
mode = "dial"
addr = "203.0.113.9:9000"
carrier = "pck"
`, "l3/pck", "iran", ""},
	} {
		var cfg config.Config
		if _, err := toml.Decode(tc.body, &cfg); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got, ok := tunnelOf(tc.name, cfg)
		if !ok {
			t.Fatalf("%s: the tunnel is not listed", tc.name)
		}
		if got.Transport != tc.transport || got.Role != tc.role {
			t.Errorf("%s: listed as %s %s, want %s %s", tc.name, got.Role, got.Transport, tc.role, tc.transport)
		}
		switch {
		case tc.reason == "" && got.Unrunnable != "":
			t.Errorf("%s: a tunnel that can run is listed as unable to: %s", tc.name, got.Unrunnable)
		case tc.reason != "" && !strings.Contains(got.Unrunnable, tc.reason):
			t.Errorf("%s: the reason %q does not say %q", tc.name, got.Unrunnable, tc.reason)
		}
	}
}
