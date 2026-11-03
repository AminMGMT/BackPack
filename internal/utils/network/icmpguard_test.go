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
	rule := strings.Join(icmpEchoRule(port, spoofReplyMark), " ")

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
	other := strings.Join(icmpEchoRule(9999, spoofReplyMark), " ")
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
	rule := strings.Join(icmpEchoRule(port, spoofReplyMark), " ")
	if !strings.Contains(rule, "@4>>16=") {
		t.Fatal("the rule lost its identifier match")
	}
	// The port is a uint16, so the match value must be one too — a value the
	// echo header's 16-bit identifier field can actually hold.
	if port == 0 {
		t.Error("the carrier derived port 0, which the guard cannot distinguish from an unset identifier")
	}
}

// In reply mode the carrier's own uplink is Echo Replies carrying the same
// identifier the rule matches, so the rule must exempt them by the mark the
// carrier stamps. Without the exemption the guard drops the tunnel it was
// installed for. The rule carries whatever mark it is given — the constant is
// only the preferred candidate, not a promise (see pickICMPReplyMark).
func TestICMPEchoRuleExemptsTheCarriersMarkedReplies(t *testing.T) {
	rule := strings.Join(icmpEchoRule(13000, spoofReplyMark), " ")
	if !strings.Contains(rule, "-m mark --mark 0x0") {
		t.Fatalf("the rule does not restrict to unmarked replies, so it would drop the tunnel's own marked ones:\n%s", rule)
	}
	// The exemption is "not marked at all", so it does not depend on which mark
	// the host turned out to allow — two carriers with different marks cannot
	// drop each other's packets.
	if other := strings.Join(icmpEchoRule(13000, 0x2), " "); other != rule {
		t.Fatalf("the rule depends on the mark's value:\n%s\n%s", rule, other)
	}
	// A side with no Echo Replies of its own needs no exemption at all.
	plain := strings.Join(icmpEchoRule(13000, 0), " ")
	if strings.Contains(plain, "-m mark") {
		t.Fatalf("a markless guard still exempts a mark:\n%s", plain)
	}
}

// The mark is not trusted to be safe: an fwmark rule can match by mask and can
// be inverted, so the guard's choice has to follow the routing outcome the
// kernel reports for each candidate, not the constant's distinctiveness.
func TestChooseICMPReplyMarkFollowsTheRoutingOutcome(t *testing.T) {
	candidates := icmpReplyMarkCandidates()
	if len(candidates) < 2 || candidates[0] != spoofReplyMark {
		t.Fatalf("candidates = %#v, want the preferred constant first", candidates)
	}
	for _, m := range candidates {
		if m == 0 {
			t.Fatal("0 is a candidate, but a zero mark is how the rule says there is no exemption")
		}
	}

	// `fwmark 0x1/0x1` catches every odd mark — the constant included. An even
	// mark is still routed as unmarked traffic is, and must be the choice.
	oddRouted := func(m uint32) bool { return m&1 == 0 }
	if got, ok := chooseICMPReplyMark(oddRouted); !ok || got&1 != 0 {
		t.Fatalf("under fwmark 0x1/0x1 chose %#x (ok=%v), want an even mark", got, ok)
	}
	// The inverted shape can just as easily route the even marks (that is
	// `not fwmark 0x0/0x1`'s complement), and then it is the odd constant that
	// keeps the route — the choice must follow the host, not a fixed side.
	evenRouted := func(m uint32) bool { return m&1 == 1 }
	if got, ok := chooseICMPReplyMark(evenRouted); !ok || got != spoofReplyMark {
		t.Fatalf("under a rule routing even marks chose %#x (ok=%v), want the odd constant", got, ok)
	}
	// A policy that reroutes every marked packet leaves nothing safe: the
	// carrier must be told so, rather than stamping a mark that breaks it.
	if got, ok := chooseICMPReplyMark(func(uint32) bool { return false }); ok {
		t.Fatalf("chose %#x when no mark keeps the route", got)
	}
}

// A guard that could not install must say why: the carrier logs that reason,
// and "the kernel's replies are suppressed" was the one thing an operator could
// not otherwise tell was false.
func TestICMPEchoGuardRecordsWhyItDidNotInstall(t *testing.T) {
	t.Setenv("PATH", "")
	g := installICMPEchoGuard(65521, 0)
	if g.Installed() {
		t.Fatal("the guard claims to have installed without iptables in PATH")
	}
	if !strings.Contains(g.note, "iptables") {
		t.Fatalf("install failure note = %q, want it to name iptables", g.note)
	}
	g.remove()
}

// The guard is chosen from both directions, which need not match: only a side
// that accepts Echo Requests has a kernel reply to suppress, and only a side
// whose own uplink is Echo Replies collides with the rule.
func TestICMPEchoGuardPlanFollowsBothDirections(t *testing.T) {
	for _, tc := range []struct {
		name               string
		send, recv         SpoofProfile
		sendType, recvType byte
		suppress, marked   bool
	}{
		{"request/request, either side", SpoofProfileICMP, SpoofProfileICMP, icmpTypeEchoRequest, icmpTypeEchoRequest, true, false},
		{"reply mode, server", SpoofProfileICMP, SpoofProfileICMP, icmpTypeEchoReply, icmpTypeEchoRequest, true, true},
		{"reply mode, client", SpoofProfileICMP, SpoofProfileICMP, icmpTypeEchoRequest, icmpTypeEchoReply, false, false},
		{"send udp, receive icmp requests", SpoofProfileUDP, SpoofProfileICMP, icmpTypeEchoRequest, icmpTypeEchoRequest, true, false},
		{"send icmpv6 replies, receive icmp requests", SpoofProfileICMPv6, SpoofProfileICMP, icmpv6TypeEchoReply, icmpTypeEchoRequest, true, false},
		{"receive icmpv6 requests", SpoofProfileICMP, SpoofProfileICMPv6, icmpTypeEchoRequest, icmpv6TypeEchoRequest, false, false},
	} {
		got := planICMPEchoGuard(tc.send, tc.recv, tc.sendType, tc.recvType)
		if got.suppress != tc.suppress || got.marked != tc.marked {
			t.Errorf("%s: plan = %+v, want suppress=%v marked=%v", tc.name, got, tc.suppress, tc.marked)
		}
	}
}
