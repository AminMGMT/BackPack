package transport

import (
	"context"
	"math"
	"math/rand"
	"time"
)

// backoff paces reconnect attempts.
//
// The old behaviour retried on a fixed interval — every second, forever. On a
// route that is down for a while that is pure waste: a steady stream of dial
// attempts that also looks like a scan to anything watching. Exponential
// backoff keeps the fast recovery (the first retry is still one interval away)
// but stretches the gap as an outage persists, so a server that stays down is
// probed a handful of times a minute, not sixty.
//
// A backoff lives for one reconnect loop: it is created when the client starts
// trying to (re)establish the control channel and discarded once connected, so
// every fresh outage starts fast again.
type backoff struct {
	min, max time.Duration
	attempt  int
}

// newBackoff starts from the configured retry interval (the floor) and caps the
// gap so failover between endpoints never stalls for long.
func newBackoff(interval time.Duration) *backoff {
	if interval <= 0 {
		interval = time.Second
	}
	max := 30 * time.Second
	if interval > max {
		max = interval
	}
	return &backoff{min: interval, max: max}
}

// Wait sleeps for the current backoff, then grows it. It returns false if ctx
// was cancelled during the wait, so the caller can stop promptly.
func (b *backoff) Wait(ctx context.Context) bool {
	d := b.duration()
	b.attempt++
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// duration is min·2^attempt, capped at max, with ±20% jitter so many clients
// reconnecting after the same outage do not synchronise into a thundering herd.
func (b *backoff) duration() time.Duration {
	d := float64(b.min) * math.Pow(2, float64(b.attempt))
	if d > float64(b.max) {
		d = float64(b.max)
	}
	d += d * 0.2 * (rand.Float64()*2 - 1)
	if d < float64(b.min) {
		d = float64(b.min)
	}
	return time.Duration(d)
}

// reset returns to the floor, e.g. after a successful connection.
func (b *backoff) reset() { b.attempt = 0 }
