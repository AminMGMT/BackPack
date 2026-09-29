package network

import (
	"strings"
	"testing"
)

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
	//
	// It calls the allocator rather than restating its arithmetic. Doing the
	// latter is why this passed while the tunnel was dropping: the copy here
	// stayed correct for the first span of carriers, which is all the test ever
	// asked for, while the real one wrapped onto live ports beyond it. See
	// TestChurningThePoolNeverStealsTheControlChannelPort.
	resetPckPorts()
	defer resetPckPorts()

	const want = pckPortSpan
	seen := make(map[uint16]int, want)
	for i := 0; i < want; i++ {
		p, err := nextPckClientPort(base)
		if err != nil {
			t.Fatalf("allocation %d was refused: %v", i, err)
		}
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

// While a tunnel has a carrier open, every carrier it opens takes its port from
// the same range — one set of firewall rules for the pool.
func TestPckClientPortBaseIsStableWhileTheTunnelIsOpen(t *testing.T) {
	resetPckPorts()
	defer resetPckPorts()
	const token = "stable-token-aaaaaaaaaaaaaaaaaaa"
	first := pckClientPortBase(token)
	held, err := nextPckClientPort(first)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if got := pckClientPortBase(token); got != first {
			t.Fatalf("base moved while a carrier held a port: %d then %d", first, got)
		}
	}
	other := pckClientPortBase("a-different-token-bbbbbbbbbbbbbb")
	if other < first+pckPortSpan && first < other+pckPortSpan {
		t.Fatalf("two tunnels were given overlapping ranges: %d and %d", first, other)
	}
	releasePckClientPort(pckTunnelID(token), held)
}

// Reported on v1.8.4: a direct pck tunnel stopped after about a day, restarts
// did not bring it back, and deleting it and making it again did. The source
// port came from the token, so every restart sent the very flow the path had
// stopped passing. Once a tunnel's carriers have all closed — a restart, or a
// reopen after the handshakes stop being answered — the next one must not come
// back on the same ports.
func TestAReopenedTunnelSendsFromNewPorts(t *testing.T) {
	resetPckPorts()
	defer resetPckPorts()
	const token = "blocked-flow-token-ccccccccccccc"
	id := pckTunnelID(token)

	moved := 0
	for run := 0; run < 20; run++ {
		base := pckClientPortBase(token)
		port, err := nextPckClientPort(base)
		if err != nil {
			t.Fatal(err)
		}
		releasePckClientPort(id, port) // the tunnel's only carrier closes
		if pckClientPortBase(token) != base {
			moved++
		}
	}
	if moved < 19 {
		t.Fatalf("a reopened tunnel came back on the same range %d times in 20", 20-moved)
	}
	if legacy := legacyPckClientPortBase(token); legacy < 32768 || int(legacy)+pckPortSpan-1 > 65535 {
		t.Fatalf("the legacy range %d is not where v1.8.4 put it", legacy)
	}
}

// The tag names the tunnel without naming its token.
func TestPckRulesAreTaggedByTunnelNotByToken(t *testing.T) {
	const token = "secret-token-dddddddddddddddddddd"
	id := pckTunnelID(token)
	for _, r := range pckRules(id, 40000, 40127) {
		c := ""
		for i := range r {
			if r[i] == "--comment" {
				c = r[i+1]
			}
		}
		if !strings.HasPrefix(c, pckRulePrefix(id)) || strings.Contains(c, token) {
			t.Errorf("rule comment %q", c)
		}
	}
}

// A crashed run's rules are found by the tunnel's tag whatever ports they were
// written for, and nothing of another tunnel's is touched.
func TestLeftoverRulesAreFoundByTheTunnelTag(t *testing.T) {
	listing := `-P OUTPUT ACCEPT
-A OUTPUT -p tcp -m tcp --sport 41000:41127 --tcp-flags RST RST -m comment --comment backpack-pck-aabbccdd-41000:41127 -j DROP
-A OUTPUT -p tcp -m tcp --sport 50000:50127 --tcp-flags RST RST -m comment --comment backpack-pck-11223344-50000:50127 -j DROP
-A OUTPUT -p tcp -m tcp --sport 42000:42127 -m comment --comment "backpack-pck-aabbccdd-42000:42127" -j NOTRACK
-A INPUT -p tcp --dport 22 -j ACCEPT`
	got := tunnelRuleDeletions(listing, pckRulePrefix("aabbccdd"))
	if len(got) != 2 {
		t.Fatalf("found %d rules to delete, want the tunnel's 2: %v", len(got), got)
	}
	for _, d := range got {
		if d[0] != "-D" || d[1] != "OUTPUT" {
			t.Errorf("not a delete of the listed rule: %v", d)
		}
	}
}

// The firewall rules must be written against the whole range, not one port, or
// the carriers above the first would send RSTs the guard does not catch.
func TestPckRulesCoverTheWholeRange(t *testing.T) {
	rules := pckRules("aabbccdd", 40000, 40127)
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
