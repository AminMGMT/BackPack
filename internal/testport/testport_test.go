package testport

import "testing"

// Two test binaries running at once must not walk the same ports.
//
// They used to: the sequence started from the clock in milliseconds, and
// `go test ./...` starts a binary per package — several within the same
// millisecond. Both then picked the same number moments apart, IsFree said yes
// to both because it was true for both, and one package's traffic arrived at
// another's listener. It surfaced as "no reply came back through the udp
// forwarder", under -race and on CI, where the binaries are slow enough to
// overlap.
func TestConcurrentBinariesStartInDifferentPlaces(t *testing.T) {
	// Neighbouring process ids are the realistic case: a test run starts its
	// binaries one after another.
	seen := map[int]int{}
	for pid := 1000; pid < 1200; pid++ {
		at := startFor(pid)
		if at < low || at >= high {
			t.Fatalf("startFor(%d) = %d, outside [%d,%d)", pid, at, low, high)
		}
		if prev, dup := seen[at]; dup {
			t.Errorf("pids %d and %d start on the same port %d", prev, pid, at)
		}
		seen[at] = pid
	}

	// And consecutive ids are not adjacent, or two binaries a port apart would
	// collide as soon as either allocated a second one.
	if d := startFor(1001) - startFor(1000); d > -64 && d < 64 {
		t.Errorf("consecutive pids start %d ports apart; they would run into each other", d)
	}
}

// Free still does what it says: a port in the range, never handed out twice.
func TestFreeHandsOutDistinctPortsInRange(t *testing.T) {
	seen := map[int]bool{}
	for i := 0; i < 20; i++ {
		p := Free(t)
		if p < low || p >= high {
			t.Fatalf("Free() = %d, outside [%d,%d)", p, low, high)
		}
		if seen[p] {
			t.Fatalf("Free() handed out %d twice", p)
		}
		seen[p] = true
	}
}
