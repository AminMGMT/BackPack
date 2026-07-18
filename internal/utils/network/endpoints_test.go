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
}
