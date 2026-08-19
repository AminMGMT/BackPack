package l3

import (
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/tunnel/mssclamp"
)

// The clamp is the fix for the one failure a layer-3 tunnel produces that every
// liveness check passes through: small packets arrive, so ping and the
// handshake and the panel all say the tunnel is fine, and every large transfer
// stalls on its first full-sized segment.

func TestClampIsDerivedFromTheMTU(t *testing.T) {
	// Automatic: the MTU minus the headers that ride outside the segment.
	if got := mssclamp.For(0, 1400, mssclamp.OverheadV4); got != 1360 {
		t.Errorf("automatic IPv4 clamp for mtu 1400 = %d, want 1360", got)
	}
	if got := mssclamp.For(0, 1400, mssclamp.OverheadV6); got != 1340 {
		t.Errorf("automatic IPv6 clamp for mtu 1400 = %d, want 1340", got)
	}
	// Explicit wins, and is not adjusted for the family: an operator who was
	// handed a number by a path-MTU measurement means that number.
	if got := mssclamp.For(1208, 1400, mssclamp.OverheadV4); got != 1208 {
		t.Errorf("explicit clamp = %d, want 1208", got)
	}
}

// The rule shape itself is the shared package's business and is tested there.
// What matters here is that a layer-3 tunnel asks for rules at all, on both
// chains and both families.
func TestClampCoversRoutedAndLocalTraffic(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range mssclamp.Rules("l3", "bp0", 1400, 0) {
		seen[r.Cmd+" "+r.Chain] = true
		if !strings.Contains(strings.Join(r.Args("-A"), " "), "backpack-l3-mss-bp0") {
			t.Errorf("a layer-3 rule is not tagged as one: %v", r.Args("-A"))
		}
	}
	// FORWARD alone would leave connections that start on this host unclamped,
	// and the forwarded ports are exactly those.
	for _, want := range []string{
		"iptables FORWARD", "iptables OUTPUT",
		"ip6tables FORWARD", "ip6tables OUTPUT",
	} {
		if !seen[want] {
			t.Errorf("no rule for %s", want)
		}
	}
}

// Turning it off must produce no rules at all, for a host whose firewall is
// managed somewhere else.
func TestClampCanBeTurnedOff(t *testing.T) {
	// Apply and Remove return without touching iptables. What is checked here
	// is that the decision is reachable without running any command.
	mssclamp.Apply("l3", "bp0", 1400, mssclamp.Off, nil)
	mssclamp.Remove("l3", "bp0", 1400, mssclamp.Off)
}

// An MTU too small to leave room for a segment must not produce a rule with a
// nonsensical size.
func TestNoClampWhenNothingFits(t *testing.T) {
	for _, f := range mssclamp.Rules("l3", "bp0", 30, 0) {
		if f.MSS <= 0 {
			t.Fatalf("a rule was built with mss %d", f.MSS)
		}
	}
}

// The config must refuse a clamp that cannot fit, rather than installing one
// that leaves the stall in place while looking like it was dealt with.
func TestConfigRejectsAnImpossibleClamp(t *testing.T) {
	base := func() Config {
		return Config{
			Mode: ModeDial, Addr: "1.2.3.4:9000", Token: "t",
			LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
		}
	}

	cfg := base()
	cfg.MSSClamp = 1400 // larger than the MTU can carry
	if err := cfg.Validate(); err == nil {
		t.Error("a clamp larger than the MTU was accepted")
	}

	cfg = base()
	cfg.MSSClamp = -7
	if err := cfg.Validate(); err == nil {
		t.Error("a negative clamp that is not the off sentinel was accepted")
	}

	for _, ok := range []int{0, mssclamp.Off, 1208} {
		cfg = base()
		cfg.MSSClamp = ok
		if err := cfg.Validate(); err != nil {
			t.Errorf("mss_clamp %d was refused: %v", ok, err)
		}
	}
}
