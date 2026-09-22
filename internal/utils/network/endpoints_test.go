package network

import "testing"

func TestEndpointsSingle(t *testing.T) {
	e := NewEndpoints("1.2.3.4:443")
	if e.Len() != 1 {
		t.Fatalf("Len = %d, want 1", e.Len())
	}
	// Rotating a single endpoint must be a no-op, so simple tunnels are
	// completely unaffected by the failover machinery.
	for i := 0; i < 5; i++ {
		if got := e.Rotate(); got != "1.2.3.4:443" {
			t.Fatalf("Rotate = %q, want the only endpoint", got)
		}
	}
}

func TestEndpointsRotation(t *testing.T) {
	e := NewEndpoints("a:1", "b:2", "c:3")
	if e.Len() != 3 {
		t.Fatalf("Len = %d, want 3", e.Len())
	}
	want := []string{"a:1", "b:2", "c:3", "a:1"} // wraps around
	if got := e.Current(); got != want[0] {
		t.Fatalf("Current = %q, want %q", got, want[0])
	}
	for _, w := range want[1:] {
		if got := e.Rotate(); got != w {
			t.Fatalf("Rotate = %q, want %q", got, w)
		}
	}
}

func TestEndpointsDedupAndBlanks(t *testing.T) {
	e := NewEndpoints("a:1", "", "  ", "a:1", "b:2")
	if got := e.All(); len(got) != 2 || got[0] != "a:1" || got[1] != "b:2" {
		t.Fatalf("All = %v, want [a:1 b:2]", got)
	}
}

func TestEndpointsNilSafe(t *testing.T) {
	var e *Endpoints
	if e.Current() != "" || e.Rotate() != "" || e.Len() != 0 || e.All() != nil {
		t.Fatal("nil Endpoints must be safe to call")
	}
	if e.Next() != "" || e.Spread() {
		t.Fatal("nil Endpoints must be safe to call for spreading too")
	}
	e.SetSpread(true) // must not panic
}

func TestEndpointsSpreadOffIsCurrent(t *testing.T) {
	// With spreading off, Next must behave exactly like Current so existing
	// tunnels are completely unaffected.
	e := NewEndpoints("a:1", "b:2", "c:3")
	for i := 0; i < 5; i++ {
		if got := e.Next(); got != "a:1" {
			t.Fatalf("Next = %q, want the current endpoint a:1", got)
		}
	}
}

func TestEndpointsSpreadRoundRobin(t *testing.T) {
	e := NewEndpoints("a:1", "b:2", "c:3")
	e.SetSpread(true)
	want := []string{"a:1", "b:2", "c:3", "a:1", "b:2"}
	for i, w := range want {
		if got := e.Next(); got != w {
			t.Fatalf("Next #%d = %q, want %q", i, got, w)
		}
	}
	// Spreading data connections must not move the control channel.
	if got := e.Current(); got != "a:1" {
		t.Fatalf("Current = %q, want a:1 — spreading must not disturb the control endpoint", got)
	}
}

func TestEndpointsSpreadSingleEndpoint(t *testing.T) {
	// Enabling spread on a one-address tunnel must be a no-op.
	e := NewEndpoints("only:1")
	e.SetSpread(true)
	for i := 0; i < 3; i++ {
		if got := e.Next(); got != "only:1" {
			t.Fatalf("Next = %q, want only:1", got)
		}
	}
}

// The order a racer is handed.
//
// A racer given the raw list would start from the primary on every reconnect
// and undo whatever the failover had decided — which is the whole value of
// health steering and of rotation.
func TestPreferenceOrderStartsWhereTheTunnelIs(t *testing.T) {
	e := NewEndpoints("a", "b", "c")

	if got := e.InPreferenceOrder(); got[0] != "a" {
		t.Fatalf("a fresh list starts at %q, want the primary", got[0])
	}

	e.Rotate() // now on b
	got := e.InPreferenceOrder()
	if len(got) != 3 {
		t.Fatalf("got %d endpoints, want all three", len(got))
	}
	if got[0] != "b" {
		t.Fatalf("after rotating, the order starts at %q — a race would undo the "+
			"rotation on every reconnect", got[0])
	}
	// And it wraps rather than truncating: every address is still a candidate.
	if got[1] != "c" || got[2] != "a" {
		t.Fatalf("order = %v, want it to wrap round the list", got)
	}
}

// A single endpoint is the ordinary case and must come back untouched.
func TestPreferenceOrderOfOneAddress(t *testing.T) {
	e := NewEndpoints("only")
	got := e.InPreferenceOrder()
	if len(got) != 1 || got[0] != "only" {
		t.Fatalf("got %v", got)
	}
}

func TestPreferenceOrderOfNothing(t *testing.T) {
	var e *Endpoints
	if got := e.InPreferenceOrder(); got != nil {
		t.Fatalf("a nil list produced %v", got)
	}
}
