package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/backpack/backpack/config"
)

// The two kinds of tunnel v2.0.0 removed, through the path start, reload and
// `backpack check` all share (validateConfig, via CheckConfigFile). Each has
// to be refused in words that say what happened and how to fix it — not as
// "neither server nor client configuration is properly set", which is what a
// [direct] file read as an empty one would have said, and not by being moved
// onto another carrier behind the operator's back.

func writeRemovedFixture(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestARemovedDirectStreamTunnelIsRefusedByName(t *testing.T) {
	for _, role := range []string{"iran", "kharej"} {
		path := writeRemovedFixture(t, role+".toml", `[direct]
role      = "`+role+`"
addr      = "203.0.113.9:8443"
token     = "a-long-enough-token-for-a-test"
transport = "stealth"
ports     = ["443"]
sessions  = 4
`)
		err := CheckConfigFile(path)
		if !errors.Is(err, config.ErrDirectRemoved) {
			t.Fatalf("%s: a [direct] config was not refused as removed: %v", role, err)
		}
		for _, want := range []string{"removed in Backpack v2.0.0", "Setup Iran / Setup Kharej → Direct"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not say %q: %v", role, want, err)
			}
		}
		if strings.Contains(err.Error(), "neither server nor client") {
			t.Errorf("%s: the file was read as empty: %v", role, err)
		}
	}
}

func TestAnL3TunnelOnARemovedCarrierIsRefusedWithTheWayOut(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"udp", `carrier = "udp"`},
		{"quic", `carrier = "quic"`},
		// No carrier at all was a udp tunnel. It gets the same refusal, not
		// a quiet switch to a carrier the other end is not speaking.
		{"empty", ``},
	} {
		for _, mode := range []string{"dial", "listen"} {
			path := writeRemovedFixture(t, tc.name+"-"+mode+".toml", `[l3]
mode     = "`+mode+`"
addr     = "203.0.113.9:9000"
token    = "a-long-enough-token-for-a-test"
`+tc.line+`
local_ip = "10.10.0.1/30"
peer_ip  = "10.10.0.2"
`)
			err := CheckConfigFile(path)
			if err == nil {
				t.Fatalf("%s (%s): a tunnel on a removed carrier was accepted", tc.name, mode)
			}
			for _, want := range []string{"removed in Backpack v2.0.0", "pck, xdi, sni, spoof", "Edit → Carrier"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s (%s): the refusal does not say %q: %v", tc.name, mode, want, err)
				}
			}
		}
	}
}
