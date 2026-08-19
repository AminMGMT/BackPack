package manage

import (
	"strings"
	"testing"
)

// The watchdog and the health screen both judge a tunnel from the kernel's
// table of established sockets. These check that the two direct kinds are
// judged on their own terms rather than a reverse tunnel's.

func TestDirectIranHealthyWhenDialledOut(t *testing.T) {
	tunnel := Tunnel{Role: "iran", Transport: "direct/stealth", Addr: "203.0.113.9:8443"}

	// An established socket to the kharej address is the tunnel.
	pairs := [][2]string{{"192.168.1.5:55123", "203.0.113.9:8443"}}
	if healthy, known := directHealthy(tunnel, pairs); !healthy || !known {
		t.Fatalf("healthy=%v known=%v, want true/true", healthy, known)
	}

	// Nothing established: down, and that is a real answer.
	if healthy, known := directHealthy(tunnel, nil); healthy || !known {
		t.Fatalf("healthy=%v known=%v, want false/true", healthy, known)
	}

	// The right port to the wrong host must not count as the tunnel.
	other := [][2]string{{"192.168.1.5:55123", "198.51.100.1:8443"}}
	if healthy, _ := directHealthy(tunnel, other); healthy {
		t.Fatal("an unrelated connection on the same port was taken for the tunnel")
	}
}

// The kharej side listens. Judging it by the dial test — which is what
// happened before, because it is not called "server" — made a perfectly
// healthy tunnel show permanently offline.
func TestDirectKharejHealthyWhenDialledInto(t *testing.T) {
	tunnel := Tunnel{Role: "kharej", Transport: "direct/wss", Addr: "0.0.0.0:8443"}

	pairs := [][2]string{{"203.0.113.9:8443", "198.51.100.4:41022"}}
	if healthy, known := directHealthy(tunnel, pairs); !healthy || !known {
		t.Fatalf("healthy=%v known=%v, want true/true", healthy, known)
	}
	if healthy, known := directHealthy(tunnel, nil); healthy || !known {
		t.Fatalf("healthy=%v known=%v, want false/true", healthy, known)
	}
}

// A layer-3 tunnel holds no TCP socket on any carrier, so the socket table can
// never answer for it. With no snapshot from the engine either, the check must
// say it does not know — and the watchdog must read that as "leave it alone"
// rather than as "restart it", which is the difference between a quiet tunnel
// and one restarted every few seconds forever.
func TestL3HealthIsUnknowableWithoutASnapshot(t *testing.T) {
	for _, carrier := range []string{"l3/udp", "l3/pck", "l3/xdi", "l3/spoof"} {
		tunnel := Tunnel{Role: "iran", Transport: carrier, Addr: "203.0.113.9:9000"}
		if _, known := directHealthy(tunnel, nil); known {
			t.Fatalf("%s: the check claimed to know with no snapshot", carrier)
		}
		if !tunnelHealthy(tunnel, nil) {
			t.Fatalf("%s: the watchdog would restart a tunnel it cannot see", carrier)
		}
	}
}

// The watchdog must never restart a tunnel it cannot observe, or it would
// restart it forever.
func TestWatchdogLeavesUnobservableTunnelsAlone(t *testing.T) {
	l3 := Tunnel{Role: "iran", Transport: "l3/pck", Addr: "203.0.113.9:9000"}
	if !tunnelHealthy(l3, nil) {
		t.Fatal("the watchdog would restart a layer-3 tunnel it cannot see")
	}

	// A direct tunnel that genuinely is down must still be reported down, or
	// the watchdog could never recover one.
	down := Tunnel{Role: "iran", Transport: "direct/tcp", Addr: "203.0.113.9:8443"}
	if tunnelHealthy(down, nil) {
		t.Fatal("a disconnected direct tunnel was reported healthy")
	}
}

// The health screen must not show green for something it did not look at.
func TestHealthDetailWording(t *testing.T) {
	iran := Tunnel{Role: "iran", Transport: "direct/tcp"}
	kharej := Tunnel{Role: "kharej", Transport: "direct/tcp"}

	if got := directStateDetail(iran, true, true); got != "peer connected" {
		t.Fatalf("connected detail = %q", got)
	}
	if got := directStateDetail(iran, false, true); got == "" ||
		got == "running, but no client is connected yet" {
		t.Fatalf("the iran side got the reverse tunnel's wording: %q", got)
	}
	if got := directStateDetail(kharej, false, true); got == "" ||
		got == "running, but not connected to the server" {
		t.Fatalf("the kharej side got the reverse tunnel's wording: %q", got)
	}
	if got := directStateDetail(iran, true, false); got == "peer connected" {
		t.Fatal("an unanswerable check claimed the peer was connected")
	}
}

// A reverse tunnel must be untouched by any of this.
func TestReverseTunnelsAreNotTreatedAsDirect(t *testing.T) {
	for _, transport := range []string{"tcp", "tcpmux", "wss", "kcp", "spoof", "pck"} {
		if IsDirectKind(Tunnel{Transport: transport}) {
			t.Fatalf("reverse transport %q was taken for a direct tunnel", transport)
		}
	}
}

// A layer-3 tunnel must report a real state, not "unknown".
//
// The web panel has no rendering for a state it does not know, so an unknown
// tunnel arrives as a grey card with a ping and a peer and no word about
// whether it is up — which is what was seen in the field. The engine writes
// its peer to the metrics snapshot precisely so this can answer.
func TestL3HealthReadsTheReportedPeer(t *testing.T) {
	tunnel := Tunnel{Role: "iran", Transport: "l3/pck", Addr: "203.0.113.9:9000"}

	// With no snapshot at all the honest answer is still unknown — but that is
	// now the narrow case of a tunnel that has not reported yet, rather than
	// every layer-3 tunnel forever.
	if _, known := directHealthy(tunnel, nil); known {
		t.Fatal("a tunnel with no snapshot claimed to know its state")
	}

	// And the wording must not pretend the check is blind to the whole kind.
	detail := directStateDetail(tunnel, false, false)
	if strings.Contains(detail, "cannot observe") {
		t.Fatalf("the unknown wording still blames the check: %q", detail)
	}
}
