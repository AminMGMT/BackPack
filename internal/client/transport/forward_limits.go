package transport

import (
	"context"
	"net"

	"golang.org/x/time/rate"
)

// forwardBandwidth is shared by every ingress flow of one transport instance,
// so bandwidth_mbps is a tunnel-wide cap rather than a per-user multiplier.
type forwardBandwidth struct{ bucket *rate.Limiter }

func newForwardBandwidth(mbps int) *forwardBandwidth {
	if mbps <= 0 {
		return nil
	}
	bytesPerSecond := float64(mbps) * 1_000_000 / 8
	return &forwardBandwidth{bucket: rate.NewLimiter(rate.Limit(bytesPerSecond), int(bytesPerSecond))}
}

func (l *forwardBandwidth) wrap(conn net.Conn) net.Conn {
	if l == nil {
		return conn
	}
	return &forwardLimitedConn{Conn: conn, limit: l}
}

func (l *forwardBandwidth) wait(n int) {
	if l == nil || n <= 0 {
		return
	}
	burst := l.bucket.Burst()
	for n > 0 {
		chunk := n
		if chunk > burst {
			chunk = burst
		}
		_ = l.bucket.WaitN(context.Background(), chunk)
		n -= chunk
	}
}

type forwardLimitedConn struct {
	net.Conn
	limit *forwardBandwidth
}

func (c *forwardLimitedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.limit.wait(n)
	return n, err
}

func (c *forwardLimitedConn) Write(p []byte) (int, error) {
	c.limit.wait(len(p))
	return c.Conn.Write(p)
}
