package network

import "testing"

// The bug this guards against cost the pck transport every tunnel it ever
// carried, and it was invisible from this side of the wire.
//
// kcp-go's listener demultiplexes sessions purely on the sender's address:
// `l.sessions[addr.String()]`. When every carrier in a client's pool sent from
// one source port, all of them reached the server as the same peer — and
// kcp-go's rule for a packet announcing a new conversation on an existing entry
// is to close the entry. Each pool connection therefore killed the session
// before it, the control channel included, and the tunnel spent its life in a
// connect/timeout/restart loop.
//
// So: carriers must take different ports, and those ports must stay inside the
// range the firewall rules are written against.
func TestPckClientPortsAreDistinctWithinTheGuardedRange(t *testing.T) {
	const token = "a-tunnel-token-for-this-test-123"
	base := pckClientPortBase(token)

	// Ask for more ports than any pool would open, to prove the allocation keeps
	// handing out distinct ones rather than repeating immediately.
	const want = pckPortSpan
	seen := make(map[uint16]int, want)
	for i := 0; i < want; i++ {
		p := base + uint16(pckPortSeq.Add(1)-1)%pckPortSpan
		seen[p]++
		if p < base || p > base+pckPortSpan-1 {
			t.Fatalf("port %d fell outside the guarded range %d-%d", p, base, base+pckPortSpan-1)
		}
	}
	if len(seen) != want {
		for p, n := range seen {
			if n > 1 {
				t.Errorf("port %d was handed out %d times", p, n)
			}
		}
		t.Fatalf("%d allocations produced only %d distinct ports — sessions would collide on the server", want, len(seen))
	}
}

// The range must not run off the end of the ephemeral range it is placed in,
// or the top carriers would be given ports outside it.
func TestPckClientPortBaseLeavesRoomForTheSpan(t *testing.T) {
	for _, token := range []string{"", "a", "short", "a-much-longer-tunnel-token-value", "\x00\xff"} {
		base := pckClientPortBase(token)
		if base < 32768 {
			t.Errorf("token %q gave base %d, below the ephemeral range", token, base)
		}
		if int(base)+pckPortSpan-1 > 65535 {
			t.Errorf("token %q gave base %d, whose span runs past 65535", token, base)
		}
	}
}

// The same token must keep giving the same range across restarts, or every
// reconnect would leave a fresh set of firewall rules behind.
func TestPckClientPortBaseIsStableForAToken(t *testing.T) {
	const token = "stable-token-aaaaaaaaaaaaaaaaaaa"
	first := pckClientPortBase(token)
	for i := 0; i < 8; i++ {
		if got := pckClientPortBase(token); got != first {
			t.Fatalf("base moved between calls: %d then %d", first, got)
		}
	}
	if other := pckClientPortBase("a-different-token-bbbbbbbbbbbbbb"); other == first {
		t.Fatal("two different tokens produced the same port range")
	}
}

// The firewall rules must be written against the whole range, not one port, or
// the carriers above the first would send RSTs the guard does not catch.
func TestPckRulesCoverTheWholeRange(t *testing.T) {
	rules := pckRules(40000, 40127)
	if len(rules) == 0 {
		t.Fatal("no rules produced")
	}
	for _, r := range rules {
		var found bool
		for _, arg := range r {
			if arg == "40000:40127" {
				found = true
			}
		}
		if !found {
			t.Errorf("rule %v does not name the port range", r)
		}
	}
	// A single port is still written bare, so the server's rules read normally.
	if got := portSpec(5050, 5050); got != "5050" {
		t.Errorf("portSpec(5050,5050) = %q, want \"5050\"", got)
	}
	if got := portSpec(40000, 40127); got != "40000:40127" {
		t.Errorf("portSpec range = %q", got)
	}
}
