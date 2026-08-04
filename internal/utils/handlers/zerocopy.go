package handlers

import (
	"fmt"
	"sync/atomic"
)

// Zero-copy forwarding is off until somebody asks for it.
//
// The kernel path is the fastest thing in this system and the least proven: it
// moves the bytes of every forwarded connection without the process ever seeing
// them, which is exactly why it is quick and exactly why a mistake in it would
// be invisible until traffic was already corrupt. The buffered path it replaces
// has carried real traffic for a long time.
//
// So it is opt-in, per tunnel. An operator with a busy tunnel and a spare one
// can turn it on for the spare, watch the counters below, and leave the busy
// one alone — rather than choosing between the speed and the risk for
// everything at once.
//
// It is also purely local. Nothing about it appears on the wire, so the two
// ends of a tunnel need not agree: one may use the kernel while the other
// copies in user space, and neither can tell. That is what makes it safe to
// enable on one side first.

var zeroCopy atomic.Bool

// SetZeroCopy records whether forwarded connections may be relayed by the
// kernel. The engine calls it once, before any connection exists.
func SetZeroCopy(on bool) { zeroCopy.Store(on) }

// ZeroCopy reports whether the kernel path is permitted.
func ZeroCopy() bool { return zeroCopy.Load() }

// What actually happened, so the choice can be checked rather than assumed.
//
// "Enabled" does not mean "used": the kernel path needs two plain TCP sockets
// and a kernel willing to splice them, and it silently declines otherwise — on
// a mux or websocket transport, on a bandwidth-limited tunnel, off Linux. An
// operator who turned it on deserves to know which of those happened, and a
// bug report saying "enabled, and every connection fell back" is worth far more
// than one saying "I turned it on".
var (
	splicedConns  atomic.Uint64
	bufferedConns atomic.Uint64
)

func countSpliced()  { splicedConns.Add(1) }
func countBuffered() { bufferedConns.Add(1) }

// RelayCounts reports how many connection directions each path has carried.
func RelayCounts() (spliced, buffered uint64) {
	return splicedConns.Load(), bufferedConns.Load()
}

// RelaySummary is the one-line answer to "is it actually being used?".
func RelaySummary() string {
	s, b := RelayCounts()
	switch {
	case !ZeroCopy():
		return fmt.Sprintf("off (%d transfers, all buffered)", b)
	case s == 0 && b == 0:
		return "on, nothing forwarded yet"
	case s == 0:
		return fmt.Sprintf("on but never used — %d transfers all fell back to the buffered path", b)
	default:
		return fmt.Sprintf("on — %d transfers zero-copy, %d buffered", s, b)
	}
}
