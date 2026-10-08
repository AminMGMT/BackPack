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
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

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
func (l *Limiter) Wrap(ctx context.Context, conn net.Conn) net.Conn {
	if l == nil || l.bucket == nil {
		return conn
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	return &limitedConn{Conn: conn, bucket: l.bucket, ctx: ctx, cancel: cancel}
}

// limitedConn paces a connection's reads and writes against a shared token
// bucket, so the cap covers the tunnel as a whole rather than each connection
// separately.
type limitedConn struct {
	net.Conn
	bucket *rate.Limiter
	// Pacing ends with either the tunnel or this connection. A dropped session
	// must release its slots while the rest of the tunnel keeps running.
	ctx           context.Context
	cancel        context.CancelFunc
	deadlineMu    sync.Mutex
	readDeadline  pacingDeadline
	writeDeadline pacingDeadline
}

func (c *limitedConn) Close() error {
	c.cancel()
	return c.Conn.Close()
}

func (c *limitedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		if waitErr := waitWithDeadline(c.ctx, c.bucket, n, &c.readDeadline); err == nil {
			err = waitErr
		}
	}
	return n, err
}

func (c *limitedConn) Write(b []byte) (int, error) {
	if err := waitWithDeadline(c.ctx, c.bucket, len(b), &c.writeDeadline); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}

// pacingDeadline wakes an in-flight token wait when its socket deadline changes.
// Updating a deadline never cancels or requeues that operation's reservation.
type pacingDeadline struct {
	mu      sync.Mutex
	when    time.Time
	changed chan struct{}
}

func (d *pacingDeadline) set(when time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.when = when
	if d.changed != nil {
		close(d.changed)
		d.changed = nil
	}
}

func (d *pacingDeadline) value() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.when
}

func (d *pacingDeadline) snapshot() (time.Time, <-chan struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.changed == nil {
		d.changed = make(chan struct{})
	}
	return d.when, d.changed
}

func (c *limitedConn) SetDeadline(when time.Time) error {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	if err := c.Conn.SetDeadline(when); err != nil {
		return err
	}
	c.readDeadline.set(when)
	c.writeDeadline.set(when)
	return nil
}

func (c *limitedConn) SetReadDeadline(when time.Time) error {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	if err := c.Conn.SetReadDeadline(when); err != nil {
		return err
	}
	c.readDeadline.set(when)
	return nil
}

func (c *limitedConn) SetWriteDeadline(when time.Time) error {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	if err := c.Conn.SetWriteDeadline(when); err != nil {
		return err
	}
	c.writeDeadline.set(when)
	return nil
}

func waitWithDeadline(ctx context.Context, bucket *rate.Limiter, n int, deadline *pacingDeadline) error {
	burst := bucket.Burst()
	for n > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		when := deadline.value()
		now := time.Now()
		if !when.IsZero() && !now.Before(when) {
			return os.ErrDeadlineExceeded
		}
		chunk := n
		if burst > 0 && chunk > burst {
			chunk = burst
		}
		reservation := bucket.ReserveN(now, chunk)
		if !reservation.OK() {
			return fmt.Errorf("rate: request of %d exceeds burst %d", chunk, burst)
		}
		if delay := reservation.DelayFrom(now); delay > 0 {
			ready := now.Add(delay)
			timer := time.NewTimer(delay)
			for {
				when, changed := deadline.snapshot()
				now = time.Now()
				if err := ctx.Err(); err != nil {
					timer.Stop()
					reservation.CancelAt(now)
					return err
				}
				if !when.IsZero() && !now.Before(when) {
					timer.Stop()
					reservation.CancelAt(now)
					return os.ErrDeadlineExceeded
				}
				if !now.Before(ready) {
					timer.Stop()
					break
				}
				wake := ready
				if !when.IsZero() && when.Before(wake) {
					wake = when
				}
				timer.Reset(wake.Sub(now))
				select {
				case <-ctx.Done():
					timer.Stop()
					reservation.CancelAt(time.Now())
					return ctx.Err()
				case <-changed:
				case <-timer.C:
				}
			}
		}
		n -= chunk
	}
	return nil
}

// UnderlyingConn exposes the socket for directional EOF, without bypassing pacing.
func (c *limitedConn) UnderlyingConn() net.Conn { return c.Conn }
