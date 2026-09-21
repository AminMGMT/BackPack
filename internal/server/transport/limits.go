package transport

import (
	"context"
	"net"
	"sync/atomic"

	"golang.org/x/time/rate"
)

// Per-tunnel limits.
//
// A tunnel is often shared: several services behind one server, or several
// customers behind one panel. Without limits a single greedy connection can
// take the whole link, and a burst of connections can exhaust the pool that
// every other user depends on. These two caps are deliberately simple — a
// ceiling on concurrent forwarded connections, and a ceiling on throughput.
//
// Both are off by default. A limit nobody asked for is a bug report waiting to
// happen, so zero always means unlimited.

// Limits describes the caps applied to one tunnel.
type Limits struct {
	// MaxConnections caps how many forwarded connections may be open at once.
	// Zero means unlimited.
	MaxConnections int
	// BandwidthMbps caps total throughput across the tunnel in megabits per
	// second. Zero means unlimited.
	BandwidthMbps int
}

// limiter enforces a set of limits. The zero value enforces nothing, so a
// transport that never configures limits pays only a nil check.
type limiter struct {
	maxConns int32
	active   atomic.Int32

	bucket *rate.Limiter
}

// newLimiter builds a limiter, or nil when nothing is limited.
func newLimiter(l Limits) *limiter {
	if l.MaxConnections <= 0 && l.BandwidthMbps <= 0 {
		return nil
	}
	lim := &limiter{maxConns: int32(l.MaxConnections)}
	if l.BandwidthMbps > 0 {
		bytesPerSecond := float64(l.BandwidthMbps) * 1_000_000 / 8
		// The burst is one second's worth, so a limited tunnel still starts a
		// transfer immediately instead of trickling from the first byte.
		lim.bucket = rate.NewLimiter(rate.Limit(bytesPerSecond), int(bytesPerSecond))
	}
	return lim
}

// acquire reserves a connection slot, reporting whether one was available.
func (l *limiter) acquire() bool {
	if l == nil || l.maxConns <= 0 {
		return true
	}
	if l.active.Add(1) > l.maxConns {
		l.active.Add(-1)
		return false
	}
	return true
}

// release returns a connection slot.
func (l *limiter) release() {
	if l == nil || l.maxConns <= 0 {
		return
	}
	l.active.Add(-1)
}

// wrap applies the bandwidth cap to a connection. Without a cap the connection
// is returned untouched, so the unlimited path adds no overhead at all.
//
// ctx is the generation's: pacing has to stop when the tunnel does. See wait.
func (l *limiter) wrap(ctx context.Context, conn net.Conn) net.Conn {
	if l == nil || l.bucket == nil {
		return conn
	}
	return &limitedConn{Conn: conn, bucket: l.bucket, ctx: ctx}
}

// waitBytes charges n bytes against the bandwidth cap, blocking for as long as
// the cap requires.
//
// This is what wrap does, for a transport that never has a net.Conn to wrap.
// udp reads and writes datagrams on one shared *net.UDPConn per listener rather
// than handing out a connection per flow, so there is nothing to put a wrapper
// around and the cap has to be applied where the bytes are counted instead.
func (l *limiter) waitBytes(ctx context.Context, n int) {
	if l == nil || l.bucket == nil {
		return
	}
	waitFor(ctx, l.bucket, n)
}

// limitedConn paces a connection's reads and writes against a shared token
// bucket, so the cap covers the tunnel as a whole rather than each connection
// separately.
type limitedConn struct {
	net.Conn
	bucket *rate.Limiter
	// ctx ends with the generation. A connection being paced has to stop
	// waiting when the tunnel is torn down; see wait.
	ctx context.Context
}

func (c *limitedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.wait(n)
	}
	return n, err
}

func (c *limitedConn) Write(b []byte) (int, error) {
	c.wait(len(b))
	return c.Conn.Write(b)
}

// wait blocks long enough to keep within the configured rate.
func (c *limitedConn) wait(n int) { waitFor(c.ctx, c.bucket, n) }

// waitFor charges n bytes against a bucket. A request larger than the bucket
// can never be satisfied in one go, so it is charged in bucket-sized pieces
// rather than failing.
func waitFor(ctx context.Context, bucket *rate.Limiter, n int) {
	if ctx == nil {
		ctx = context.Background()
	}
	burst := bucket.Burst()
	for n > 0 {
		chunk := n
		if burst > 0 && chunk > burst {
			chunk = burst
		}
		// The generation's context, not a background one.
		//
		// This used to pass context.Background() with a note saying the
		// deadline that matters is the connection's own, which the underlying
		// Read/Write enforces. That is subtly wrong: WaitN blocks *before* the
		// Read or Write it is pacing, so a deadline on the socket does not
		// interrupt it and closing the connection does not either. A tunnel
		// being torn down would sit here paying out a token bucket for a
		// connection that is already going away — bounded by the bytes in hand
		// over the configured rate, which is small at realistic limits and
		// unbounded by anything the caller controls.
		//
		// An expired context returns an error here, which is the same "give up
		// and let the bytes through" path a failed reservation already took:
		// dropping them would corrupt the stream and blocking would hang it.
		if err := bucket.WaitN(ctx, chunk); err != nil {
			return
		}
		n -= chunk
	}
}
