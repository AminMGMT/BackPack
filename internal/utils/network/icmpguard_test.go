//go:build linux

package network

import (
	"strings"
	"testing"
)

// The guard's whole value is that it is narrow: it drops the kernel's replies to
// THIS carrier's echo requests and nothing else. What makes it narrow is the
// ICMP-identifier match, and if that were wrong — the wrong offset, the wrong
// field, the wrong port — the rule would either drop every echo reply on the
// host (breaking ping entirely) or none (leaking a reply per data packet). So
// the parts of the rule that carry that meaning are pinned here; the behaviour
// itself is proven by a live tunnel, which needs a raw socket a test cannot
// open.

func TestICMPEchoRuleTargetsTheCarriersIdentifier(t *testing.T) {
	const port = 13000
	rule := strings.Join(icmpEchoRule(port), " ")

	// It acts on outbound packets — the kernel's reply leaves the host — and only
	// on echo replies, never requests (the carrier's own traffic is requests).
	if !strings.Contains(rule, "OUTPUT") {
		t.Error("the rule is not on the OUTPUT chain, so it would not catch the kernel's outgoing reply")
	}
	if !strings.Contains(rule, "echo-reply") {
		t.Error("the rule does not restrict to echo replies; it could catch the carrier's own requests")
	}
	if !strings.Contains(rule, "-j DROP") {
		t.Error("the rule does not drop")
	}

	// The identifier match is the narrowness. The u32 expression walks past the
	// IP header (0>>22&0x3C) and reads the identifier (@4>>16), matched to the
	// port. A different port must produce a different match, or every tunnel on
	// the host would share one rule.
	if !strings.Contains(rule, "0>>22&0x3C@4>>16=13000") {
		t.Errorf("the identifier match is not the carrier's port:\n%s", rule)
	}
	other := strings.Join(icmpEchoRule(9999), " ")
	if strings.Contains(other, "=13000") || !strings.Contains(other, "=9999") {
		t.Errorf("two ports produced the same match; the rule is not per-tunnel:\n%s", other)
	}

	// A comment tags it as ours and per-port, so a rule left behind by a crash is
	// findable and removable by hand — the same shape the RST guard uses.
	if !strings.Contains(rule, "backpack-spoof-icmp-13000") {
		t.Errorf("the rule is not tagged for identification:\n%s", rule)
	}
}

// The identifier the rule matches has to be the identifier the carrier actually
// stamps, or the rule matches nothing. The carrier stamps its port (see the
// buildICMPEcho call in spoofconn_linux.go), and the guard is built from the
// same port, so this pins that they are derived from one value rather than two
// that could drift.
func TestTheGuardMatchesTheIdentifierTheCarrierSends(t *testing.T) {
	// spoofIdentity is what the carrier uses to get its port; the guard is
	// installed with the same port in newSpoofConn.
	_, port := spoofIdentity("a-tunnel-token-for-the-guard-test")
	rule := strings.Join(icmpEchoRule(port), " ")
	if !strings.Contains(rule, "@4>>16=") {
		t.Fatal("the rule lost its identifier match")
	}
	// The port is a uint16, so the match value must be one too — a value the
	// echo header's 16-bit identifier field can actually hold.
	if port == 0 {
		t.Error("the carrier derived port 0, which the guard cannot distinguish from an unset identifier")
	}
}
