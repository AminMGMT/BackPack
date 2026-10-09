package transport

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
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	return &limitedConn{Conn: conn, bucket: l.bucket, ctx: ctx, cancel: cancel}
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
	_ = waitFor(ctx, l.bucket, n)
}

// limitedConn paces a connection's reads and writes against a shared token
// bucket, so the cap covers the tunnel as a whole rather than each connection
// separately.
type limitedConn struct {
	net.Conn
	bucket *rate.Limiter
	// ctx ends with the generation. A connection being paced has to stop
	// waiting when the tunnel is torn down; see wait.
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

// waitFor charges n bytes against a bucket. A request larger than the bucket
// can never be satisfied in one go, so it is charged in bucket-sized pieces
// rather than failing.
func waitFor(ctx context.Context, bucket *rate.Limiter, n int) error {
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
		// A failed wait ends this operation. Letting bytes through after
		// cancellation would turn teardown into an unpaced write.
		if err := bucket.WaitN(ctx, chunk); err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

// UnderlyingConn exposes the socket for directional EOF, without bypassing pacing.
func (c *limitedConn) UnderlyingConn() net.Conn { return c.Conn }
