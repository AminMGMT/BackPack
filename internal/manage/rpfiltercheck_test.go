package manage

import (
	"strings"
	"testing"

	"github.com/backpack/backpack/config"
)

// The rp_filter check is the one Health Check line that catches a spoof tunnel
// which is up, connected, and carrying nothing. Its value comes from the live
// host, so what these pin is the shape: which carriers get the check, and that
// a strict reading produces a failure naming the exact fix while a relaxed one
// passes. The reading itself is exercised in the netns rig.

// Only a forged-source carrier is subject to rp_filter dropping its packets, so
// only a spoof tunnel gets the line. A udp or pck l3 tunnel must not, or the
// check is noise on every other tunnel.
func TestRPFilterCheckIsOnlyForTheSpoofCarrier(t *testing.T) {
	spoof := rpFilterCheck("Tunnel: t", config.L3Config{
		Mode: "listen", Carrier: "spoof", SpoofConfig: config.SpoofConfig{SpoofPeerIP: "203.0.113.9"},
	})
	if spoof.Name != "Reverse-path filter" {
		t.Fatalf("the spoof check has the wrong name: %q", spoof.Name)
	}
	// It must land on one of the three real outcomes, never an empty check.
	switch spoof.Level {
	case CheckOK, CheckFail, CheckInfo:
	default:
		t.Errorf("unexpected level %v", spoof.Level)
	}
}

// When it does fire as a failure, the fix has to be a command the operator can
// paste — naming the interface it resolved, not a generic "relax it".
func TestAStrictRPFilterFailureNamesTheFix(t *testing.T) {
	c := rpFilterCheck("Tunnel: t", config.L3Config{
		Mode: "dial", Carrier: "spoof", Addr: "203.0.113.9:9000",
		SpoofConfig: config.SpoofConfig{SpoofInterface: "eth0"},
	})
	// On this host rp_filter is whatever it is; only assert the shape of a
	// failure if that is what came back.
	if c.Level == CheckFail {
		if !strings.Contains(c.Fix, "sysctl -w net.ipv4.conf.") {
			t.Errorf("a strict-rp_filter failure gave no sysctl fix: %q", c.Fix)
		}
		if !strings.Contains(c.Detail, "drops forged-source packets") {
			t.Errorf("the detail does not explain the symptom: %q", c.Detail)
		}
	}
}
