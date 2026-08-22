package network

import (
	"sync"
	"testing"
)

// The regression test for a tunnel that dropped every so often and had to be
// restarted by hand.
//
// Each of the client's pck carriers must come from its own source port: kcp-go
// demultiplexes on the sender's address alone, so two carriers sharing one port
// arrive at the server as a single peer, and a packet claiming a new
// conversation on an existing entry closes the old one. When the pool reused
// the control channel's port, it killed the control channel.
//
// The allocator was a counter taken modulo the span, which never came back
// down. It is right for the first pckPortSpan carriers and wrong for every one
// after: carrier 128 was handed carrier 0's port, and carrier 0 is the control
// channel. A pool dialling every sixteen seconds — what the field logs showed —
// reaches that in about half an hour, which is exactly how often the tunnel
// went down.

func resetPckPorts() {
	pckPortMu.Lock()
	defer pckPortMu.Unlock()
	pckPortsInUse = map[uint16]bool{}
}

func TestALivePortIsNeverHandedOutTwice(t *testing.T) {
	resetPckPorts()
	defer resetPckPorts()

	const base uint16 = 40000

	// Hold the whole span, as a long-lived control channel plus a busy pool
	// eventually would.
	held := make([]uint16, 0, pckPortSpan)
	seen := map[uint16]bool{}
	for i := 0; i < pckPortSpan; i++ {
		port, err := nextPckClientPort(base)
		if err != nil {
			t.Fatalf("carrier %d could not get a port: %v", i, err)
		}
		if seen[port] {
			t.Fatalf("carrier %d was handed port %d, which is already live", i, port)
		}
		seen[port] = true
		held = append(held, port)
	}

	// The next one must be refused, not given a port somebody else is using.
	if port, err := nextPckClientPort(base); err == nil {
		t.Fatalf("with the whole range held, another carrier was given port %d — "+
			"that port belongs to a live session, and the server would treat the two "+
			"as one peer and close the older one", port)
	}

	// Once a carrier closes, its port comes back.
	releasePckClientPort(held[0])
	port, err := nextPckClientPort(base)
	if err != nil {
		t.Fatalf("a released port was not reusable: %v", err)
	}
	if port != held[0] {
		t.Errorf("got port %d, want the released %d", port, held[0])
	}
}

// The control channel is the first carrier dialled and outlives every pool
// connection. Churning the pool past the width of the span must never come back
// round onto it.
func TestChurningThePoolNeverStealsTheControlChannelPort(t *testing.T) {
	resetPckPorts()
	defer resetPckPorts()

	const base uint16 = 40000

	control, err := nextPckClientPort(base)
	if err != nil {
		t.Fatal(err)
	}

	// Ten times round the span, one pool connection at a time: dial, use,
	// close. This is the shape of a pool over several hours.
	for i := 0; i < pckPortSpan*10; i++ {
		port, err := nextPckClientPort(base)
		if err != nil {
			t.Fatalf("pool dial %d was refused a port: %v", i, err)
		}
		if port == control {
			t.Fatalf("pool dial %d was handed the control channel's port %d, which "+
				"is what killed the tunnel and forced a manual restart", i, port)
		}
		releasePckClientPort(port)
	}
}

// Carriers are dialled from several goroutines, so the allocator has to hold up
// under that too.
func TestPortsAreUniqueUnderConcurrentDials(t *testing.T) {
	resetPckPorts()
	defer resetPckPorts()

	const base uint16 = 40000
	const dialers = pckPortSpan

	var mu sync.Mutex
	seen := map[uint16]bool{}

	var wg sync.WaitGroup
	for i := 0; i < dialers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			port, err := nextPckClientPort(base)
			if err != nil {
				t.Errorf("concurrent dial refused: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[port] {
				t.Errorf("port %d was handed to two carriers at once", port)
			}
			seen[port] = true
		}()
	}
	wg.Wait()

	if len(seen) != dialers {
		t.Errorf("%d dials produced %d distinct ports", dialers, len(seen))
	}
}

// Every port handed out has to sit inside the range the firewall rules cover,
// or the kernel's RSTs are not suppressed for it.
func TestEveryPortStaysInsideTheGuardedRange(t *testing.T) {
	resetPckPorts()
	defer resetPckPorts()

	const base uint16 = 40000
	for i := 0; i < pckPortSpan; i++ {
		port, err := nextPckClientPort(base)
		if err != nil {
			t.Fatal(err)
		}
		if port < base || port > base+pckPortSpan-1 {
			t.Fatalf("port %d is outside the guarded range %d..%d",
				port, base, base+pckPortSpan-1)
		}
	}
}
