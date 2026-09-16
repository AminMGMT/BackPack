package transport

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The pairing timeout has to fire when nothing arrives, because that is the
// only case it exists for.
//
// It was checked once, at the top of the pairing loop, and the loop then
// blocked on the tunnel channel — so it only ever ran when a tunnel connection
// arrived, and a connection that arrived is a connection that did not need
// timing out. With a pool that had run dry the accepted client was held with
// nobody waiting on it: the browser sat there until it gave up on its own, the
// slot it had taken against max_connections was never returned, and the socket
// stayed open for the life of the run.
func TestThePairingWaitAlwaysProducesAUsableTimer(t *testing.T) {
	now := nowMillis()

	// A connection that has just arrived waits out most of the timeout.
	if got := pairingWait(now); got > pairingTimeout || got < pairingTimeout-time.Second {
		t.Errorf("a fresh connection waits %v, want about %v", got, pairingTimeout)
	}
	// One that is part way through waits out the remainder.
	if got := pairingWait(now - pairingTimeout.Milliseconds()/2); got > pairingTimeout/2+time.Second {
		t.Errorf("a half-aged connection waits %v, want about %v", got, pairingTimeout/2)
	}
	// And one already past it never produces a non-positive duration.
	// time.NewTimer of zero or less fires immediately and forever, which turns
	// the pairing loop into a spin rather than a timeout.
	for _, age := range []int64{
		pairingTimeout.Milliseconds(),
		pairingTimeout.Milliseconds() + 1,
		pairingTimeout.Milliseconds() * 100,
	} {
		if got := pairingWait(now - age); got <= 0 {
			t.Errorf("a connection aged %dms produced a wait of %v — a timer built from "+
				"that spins instead of waiting", age, got)
		}
	}
}

// Every transport that parks a connection waiting to be paired has to time it
// out on a timer, and has to let it go when the run ends.
//
// Both halves used to be missing together, and they are checked together for
// the same reason: the connection is held by the pairing loop and nothing else
// on the machine knows it exists, so whichever way the loop leaves without
// dealing with it, the socket and its limit slot are gone for the life of the
// run. Every reload leaked one of each per connection parked there.
func TestEveryPairingLoopTimesOutAndCleansUpOnShutdown(t *testing.T) {
	// The transports whose pairing loop blocks on a tunnel connection. The mux
	// ones are not here on purpose: they open a stream on the session they
	// already hold, so nothing is ever parked waiting.
	for _, name := range []string{"tcp", "ws", "quic", "udp"} {
		t.Run(name, func(t *testing.T) {
			src := readTransportSource(t, name+".go")
			if !strings.Contains(src, "time.NewTimer(pairingWait(") {
				t.Errorf("%s.go waits for a tunnel connection with no timer, so a "+
					"connection parked on an empty pool is never timed out", name)
			}
			if !strings.Contains(src, "case <-timer.C:") {
				t.Errorf("%s.go builds a pairing timer and never selects on it", name)
			}
			if !cleansUpOnDone(src) {
				t.Errorf("%s.go leaves its pairing loop on ctx.Done() without releasing "+
					"the connection it is holding — one socket and one limit slot "+
					"leaked per parked connection, on every restart", name)
			}
		})
	}
}

// cleansUpOnDone reports whether every ctx.Done() branch inside a pairing loop
// does something about the connection it holds before returning.
func cleansUpOnDone(src string) bool {
	const anchor = "case <-timer.C:"
	i := strings.Index(src, anchor)
	if i < 0 {
		return false
	}
	// The shutdown branch sits immediately above the timer branch in each of
	// them; take the block between the select and it.
	j := strings.LastIndex(src[:i], "case <-g.ctx.Done():")
	if j < 0 {
		return false
	}
	block := src[j:i]
	return strings.Contains(block, "s.limits.release()") ||
		strings.Contains(block, "s.dropLocalFlow(")
}

// The timeout is one number, written once. It was written seven times as a bare
// 3000, and the number was never the part that differed.
func TestNoTransportCarriesItsOwnPairingTimeout(t *testing.T) {
	for _, name := range []string{"tcp", "tcpmux", "ws", "wsmux", "kcp", "quic", "udp"} {
		src := readTransportSource(t, name+".go")
		if strings.Contains(src, "> 3000") {
			t.Errorf("%s.go carries its own copy of the pairing timeout", name)
		}
	}
	if _, err := os.Stat("pairing.go"); err != nil {
		t.Fatalf("the shared pairing timeout is gone: %v", err)
	}
}
