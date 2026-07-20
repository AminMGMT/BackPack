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
func (l *limiter) wrap(conn net.Conn) net.Conn {
	if l == nil || l.bucket == nil {
		return conn
	}
	return &limitedConn{Conn: conn, bucket: l.bucket}
}

// limitedConn paces a connection's reads and writes against a shared token
// bucket, so the cap covers the tunnel as a whole rather than each connection
// separately.
type limitedConn struct {
	net.Conn
	bucket *rate.Limiter
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

// wait blocks long enough to keep within the configured rate. A request larger
// than the bucket can never be satisfied in one go, so it is charged in
// bucket-sized pieces rather than failing.
func (c *limitedConn) wait(n int) {
	burst := c.bucket.Burst()
	for n > 0 {
		chunk := n
		if burst > 0 && chunk > burst {
			chunk = burst
		}
		// A background context: the deadline that matters is the connection's
		// own, which the underlying Read/Write already enforces.
		if err := c.bucket.WaitN(context.Background(), chunk); err != nil {
			return
		}
		n -= chunk
	}
}
