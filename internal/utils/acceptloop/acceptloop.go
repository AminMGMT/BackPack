// Package acceptloop keeps a failing accept loop from burning a core.
//
// Every accept loop in this program was written the same way: block in Accept,
// and on an error `continue`. That is right for the error Accept normally
// returns — one connection failed the handshake, take the next — and wrong for
// the two that matter. A listener whose socket has been closed, and a process
// that has run out of file descriptors, both return an error immediately and go
// on returning it. `continue` then spins the loop as fast as the CPU allows,
// with nothing to block on: one core pinned at 100% for as long as the
// condition lasts, which for a closed listener is forever. A context check at
// the top of the loop does not help, because the context is not cancelled —
// only the listener is broken.
//
// The close window is not hypothetical: a tunnel restart closes the listener
// and cancels the context, and whichever lands first decides whether the loop
// exits cleanly or spins until the cancellation is observed.
//
// This is the smallest fix that keeps the existing behaviour. A transient
// failure still retries, so nothing that used to recover stops recovering, but
// consecutive failures back off geometrically to a ceiling instead of retrying
// instantly. A loop that is genuinely broken then costs a wakeup every 100 ms
// rather than a whole core, and a loop that is merely rejecting bad connections
// resets on the first success and never waits at all.
//
// It lives in its own package because it was written for the reverse
// transports and the SOCKS proxy needed the same thing, with the same argument
// behind it — and socks is a leaf package that nothing else here can be
// imported into.
package acceptloop

import (
	"context"
	"time"
)

// Backoff paces the retries of one accept loop. The zero value is ready to use
// and has never waited.
type Backoff struct {
	delay time.Duration
}

const (
	// first is the pause after the first failure. Short enough to be invisible
	// to a connection that deserved a retry.
	first = 5 * time.Millisecond
	// max is the ceiling. At this rate a permanently broken loop wakes ten
	// times a second, which costs nothing measurable.
	max = 100 * time.Millisecond
)

// Fail records an accept error and waits for the current backoff. It returns
// false if the context was cancelled while waiting, so the caller can return.
func (b *Backoff) Fail(ctx context.Context) bool {
	if b.delay == 0 {
		b.delay = first
	} else if b.delay < max {
		b.delay *= 2
		if b.delay > max {
			b.delay = max
		}
	}
	t := time.NewTimer(b.delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// OK clears the backoff after a successful accept, so an occasional bad
// connection never slows the next good one.
func (b *Backoff) OK() { b.delay = 0 }
