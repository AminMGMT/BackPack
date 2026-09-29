package manage

import "testing"

// The panel's Fine Tune drawer reads the clamp off the tunnel and writes it
// back. A zero has to survive that round trip as a zero: it is the answer
// "let the kernel choose", and clearing the box is how the clamp is removed.
func TestFineTuneCarriesTheClampBothWays(t *testing.T) {
	s := TunnelSpec{Role: "server", Transport: "wss", MSS: 1208}
	if got := tuneOf(s).MSS; got != 1208 {
		t.Fatalf("the drawer would open on mss %d, not the 1208 the tunnel runs", got)
	}

	tune := tuneOf(s)
	tune.apply(&s)
	if s.MSS != 1208 {
		t.Fatalf("an untouched drawer changed the clamp to %d", s.MSS)
	}

	tune.MSS = 0
	tune.apply(&s)
	if s.MSS != 0 {
		t.Fatalf("clearing the clamp left it at %d — there would be no way to undo one", s.MSS)
	}
}
