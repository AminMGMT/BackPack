package transport

import (
	"net"
	"sync"
	"time"
)

// localQueueTimeout is the maximum time an accepted connection may wait for a
// tunnel data connection. Keeping the deadline with the connection (rather than
// checking its age only after dequeue) makes the limit real even while every
// mux stream is busy or the tunnel is temporarily unavailable.
const localQueueTimeout = 3 * time.Second

type LocalTCPConn struct {
	conn       net.Conn
	remoteAddr string
	lease      *localTCPLease
}

type localTCPLease struct {
	mu        sync.Mutex
	conn      net.Conn
	limits    *limiter
	timer     *time.Timer
	expired   chan struct{}
	onRelease func()
	state     uint8 // 0 queued, 1 claimed by a relay, 2 released
}

func newLocalTCPConn(conn net.Conn, remoteAddr string, limits *limiter) LocalTCPConn {
	return newLocalTCPConnWithTimeout(conn, remoteAddr, limits, localQueueTimeout, nil)
}

func newCountedLocalTCPConn(conn net.Conn, remoteAddr string, limits *limiter, onRelease func()) LocalTCPConn {
	return newLocalTCPConnWithTimeout(conn, remoteAddr, limits, localQueueTimeout, onRelease)
}

func newLocalTCPConnWithTimeout(conn net.Conn, remoteAddr string, limits *limiter, timeout time.Duration, onRelease func()) LocalTCPConn {
	lease := &localTCPLease{
		conn:      conn,
		limits:    limits,
		expired:   make(chan struct{}),
		onRelease: onRelease,
	}
	lease.timer = time.AfterFunc(timeout, lease.expire)
	return LocalTCPConn{
		conn:       conn,
		remoteAddr: remoteAddr,
		lease:      lease,
	}
}

func (l *localTCPLease) expire() {
	l.mu.Lock()
	if l.state != 0 {
		l.mu.Unlock()
		return
	}
	l.state = 2
	l.mu.Unlock()

	l.release()
	close(l.expired)
}

func (l *localTCPLease) release() {
	l.conn.Close()
	l.limits.release()
	if l.onRelease != nil {
		l.onRelease()
	}
}

// claim transfers responsibility for releasing the limiter slot from the
// queue timer to a relay handler. It fails when the deadline won the race.
func (c LocalTCPConn) claim() bool {
	if c.lease == nil { // compatibility for focused tests constructing literals
		return true
	}
	l := c.lease
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != 0 {
		return false
	}
	l.state = 1
	l.timer.Stop()
	return true
}

func (c LocalTCPConn) expiry() <-chan struct{} {
	if c.lease == nil {
		return nil
	}
	return c.lease.expired
}

// closeAndRelease is safe from the queue, a pairing goroutine, or the relay's
// defer. Exactly one caller closes the connection and returns its quota.
func (c LocalTCPConn) closeAndRelease() {
	if c.lease == nil {
		c.conn.Close()
		return
	}
	l := c.lease
	l.mu.Lock()
	if l.state == 2 {
		l.mu.Unlock()
		return
	}
	l.state = 2
	l.timer.Stop()
	l.mu.Unlock()

	l.release()
	close(l.expired)
}

func drainLocalTCP(ch <-chan LocalTCPConn) {
	for {
		select {
		case conn, ok := <-ch:
			if !ok {
				return
			}
			conn.closeAndRelease()
		default:
			return
		}
	}
}
