package l3

import (
	"errors"
	"sync"
	"time"

	wgtun "golang.zx2c4.com/wireguard/tun"
)

// What to do when the device says a read held more segments than there were
// buffers for.
//
// It lives here, away from the Linux device that raises it, for the reason
// clashAgainst does: the device needs a real TUN interface and root to build,
// so a test that went through it could only run on one platform and in one
// privilege. The decision itself has no operating system in it.

// segmentOverflow reports whether a device read failed only because the run it
// returned held more segments than there were buffers, and how many packets
// survived it.
//
// The distinction matters because the two look identical to a caller that only
// checks err: one is a device that has stopped working and the tunnel should be
// rebuilt, the other is a busy moment that cost some packets. Treating the
// second as the first is what put a tunnel into a restart loop.
func segmentOverflow(n int, err error) (kept int, overflow bool) {
	if !errors.Is(err, wgtun.ErrTooManySegments) {
		return 0, false
	}
	// The split reports the count it had completed before it ran out of room,
	// which is one less than the index it stopped on — and is -1 if it never
	// had room for anything at all.
	if n < 0 {
		n = 0
	}
	return n, true
}

// reportEvery limits a repeated report to one in a while.
//
// The condition it guards arrives per read: a flow that provokes it provokes it
// thousands of times a second, and a line each would bury everything else in
// the journal — including whatever an operator would need to look at next.
type reportEvery struct {
	every time.Duration

	mu    sync.Mutex
	last  time.Time
	count uint64
}

// allow records an occurrence and reports whether this one should be spoken
// about, along with the number seen so far. The first is always allowed: a
// condition nobody has been told about once is worse than one mentioned twice.
func (r *reportEvery) allow(now time.Time) (uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.count++
	if !r.last.IsZero() && now.Sub(r.last) < r.every {
		return r.count, false
	}
	r.last = now
	return r.count, true
}
