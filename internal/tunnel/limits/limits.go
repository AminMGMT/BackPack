// Package limits caps what one tunnel may use.
//
// A tunnel is often shared: several services behind one server, or several
// customers behind one panel. Without limits a single greedy connection can
// take the whole link, and a burst of connections can exhaust what every other
// user depends on. Two caps, deliberately simple — a ceiling on concurrent
// forwarded connections, and a ceiling on throughput.
//
// Both are off by default. A limit nobody asked for is a bug report waiting to
// happen, so zero always means unlimited, and an unlimited tunnel pays nothing
// but a nil check.
//
// The reverse transports have an equivalent of their own, unexported in
// internal/server/transport. This is a second implementation rather than a
// shared one because merging them would mean editing the reverse data path,
// and these caps are not worth that risk. The two are small, they do the same
// arithmetic, and either can be changed without the other.
package limits

import (
	"context"
	"net"
	"sync/atomic"

	"golang.org/x/time/rate"
)

// Config describes the caps applied to one tunnel.
type Config struct {
	// MaxConnections caps how many forwarded connections may be open at once.
	// Zero means unlimited.
	MaxConnections int

	// BandwidthMbps caps total throughput across the tunnel in megabits per
	// second. Zero means unlimited.
	BandwidthMbps int
}

// Limiter enforces a Config. A nil *Limiter enforces nothing, so every method
// is safe on one and the unlimited path costs a single comparison.
type Limiter struct {
	maxConns int32
	active   atomic.Int32
	bucket   *rate.Limiter
}

// New builds a limiter, or nil when nothing is limited.
func New(c Config) *Limiter {
	if c.MaxConnections <= 0 && c.BandwidthMbps <= 0 {
		return nil
	}
	l := &Limiter{maxConns: int32(c.MaxConnections)}
	if c.BandwidthMbps > 0 {
		bytesPerSecond := float64(c.BandwidthMbps) * 1_000_000 / 8
		// The burst is one second's worth, so a limited tunnel still starts a
		// transfer immediately instead of trickling from the first byte.
		l.bucket = rate.NewLimiter(rate.Limit(bytesPerSecond), int(bytesPerSecond))
	}
	return l
}

// Acquire reserves a connection slot, reporting whether one was available.
// Every Acquire that returns true must be paired with a Release.
func (l *Limiter) Acquire() bool {
	if l == nil || l.maxConns <= 0 {
		return true
	}
	if l.active.Add(1) > l.maxConns {
		l.active.Add(-1)
		return false
	}
	return true
}

// Release returns a connection slot.
func (l *Limiter) Release() {
	if l == nil || l.maxConns <= 0 {
		return
	}
	l.active.Add(-1)
}

// Active is how many slots are currently held, for diagnostics.
func (l *Limiter) Active() int {
	if l == nil {
		return 0
	}
	return int(l.active.Load())
}

// Wrap applies the bandwidth cap to a connection. Without a cap the connection
// is returned untouched, so the unlimited path adds no overhead at all.
func (l *Limiter) Wrap(conn net.Conn) net.Conn {
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

// wait blocks long enough to keep within the configured rate.
//
// A request larger than the bucket can never be satisfied in one go — the
// limiter refuses it outright rather than waiting — so it is charged in
// bucket-sized pieces. Without that, a single read bigger than one second's
// worth of bandwidth would fail forever instead of simply being slow.
func (c *limitedConn) wait(n int) {
	burst := c.bucket.Burst()
	for n > 0 {
		chunk := n
		if burst > 0 && chunk > burst {
			chunk = burst
		}
		// A background context: the deadline that matters is the connection's
		// own, which the underlying Read/Write already enforces. An error here
		// means the reservation could not be made at all, and there is nothing
		// useful to do but let the bytes through — dropping them would corrupt
		// the stream, and blocking forever would hang it.
		if err := c.bucket.WaitN(context.Background(), chunk); err != nil {
			return
		}
		n -= chunk
	}
}
