package transport

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// enableKeepAlive turns on TCP keepalive on a control connection so a peer that
// dies without closing — a hard kill, or a path that blackholes under load —
// eventually surfaces as a read error instead of a connection that hangs open
// forever. A no-op for anything that is not a TCP connection.
func enableKeepAlive(conn net.Conn, period time.Duration) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tcp.SetKeepAlive(true)
	_ = tcp.SetKeepAlivePeriod(period)
}

// The control channel is written by the handshake goroutine and read by the
// accept loop, the heartbeat loop and the restart path — all at the same time.
// Left as a plain field it is a data race: Go gives no guarantee about what a
// concurrent reader observes, so the accept loop can see a stale nil and reject
// connections that should have been let through, or a half-published pointer.
//
// These holders make every access explicit and synchronised. They are cheap:
// the lock is held only long enough to copy an interface value, never across a
// network call.

// sameHost reports whether two addresses share a host, ignoring the port.
//
// It compares the parsed IPs rather than their strings, so the same address
// written two ways — an IPv6 peer seen as "::1" and "0:0:0:0:0:0:0:1", or an
// IPv4-mapped IPv6 address — is recognised as one host. A nil address never
// matches anything.
func sameHost(a, b net.Addr) bool {
	if a == nil || b == nil {
		return false
	}
	ha, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return false
	}
	hb, _, err := net.SplitHostPort(b.String())
	if err != nil {
		return false
	}
	ipa, ipb := net.ParseIP(ha), net.ParseIP(hb)
	if ipa == nil || ipb == nil {
		return ha == hb // not IPs (a hostname): fall back to a literal match
	}
	return ipa.Equal(ipb)
}

// netControl holds the control channel for the transports that use a plain
// network connection (tcp, tcpmux, udp, kcp).
type netControl struct {
	mu   sync.RWMutex
	conn net.Conn
}

// Get returns the current control connection, or nil when none is established.
func (c *netControl) Get() net.Conn {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conn
}

// Set publishes a newly established control connection.
func (c *netControl) Set(conn net.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn = conn
}

// Clear forgets the control connection without closing it, used on restart
// where the caller has already dealt with the old one.
func (c *netControl) Clear() {
	c.Set(nil)
}

// IsSet reports whether a control channel is currently established.
func (c *netControl) IsSet() bool {
	return c.Get() != nil
}

// Close closes the control connection if there is one. Safe to call when there
// is not.
func (c *netControl) Close() {
	if conn := c.Get(); conn != nil {
		conn.Close()
	}
}

// RemoteAddr returns the peer address of the control channel, or nil when no
// control channel is established.
func (c *netControl) RemoteAddr() net.Addr {
	if conn := c.Get(); conn != nil {
		return conn.RemoteAddr()
	}
	return nil
}

// wsControl is the same holder for the websocket transports, which work with a
// websocket connection rather than a net.Conn.
type wsControl struct {
	mu   sync.RWMutex
	conn *websocket.Conn
}

func (c *wsControl) Get() *websocket.Conn {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conn
}

func (c *wsControl) Set(conn *websocket.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn = conn
}

func (c *wsControl) Clear() { c.Set(nil) }

func (c *wsControl) IsSet() bool { return c.Get() != nil }

func (c *wsControl) Close() {
	if conn := c.Get(); conn != nil {
		conn.Close()
	}
}

// runState holds the context of the run a transport is currently on.
//
// Restart cancels the old one, builds a new pair and installs it — and then
// starts the next run in a fresh goroutine, which reads the field back to seed
// its generation. Those two are not ordered against each other: the mutex that
// serialises Restart against Restart is released before the new run has read
// anything, so a second restart can be writing the field while the first one's
// Start is still reading it. That is the race the CI detector reported against
// the KCP transport, and every other transport in this package has the same
// pair of fields written the same way.
//
// It is behind a lock for exactly the reason netControl above is: one value
// replaced by one goroutine while several others are looking at it.
type runState struct {
	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
}

// set installs the context of a new run, replacing whatever was there.
func (r *runState) set(ctx context.Context, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctx, r.cancel = ctx, cancel
}

// context returns the current run's context. Callers seed a generation from it
// once and then use the copy, so a later restart cannot move the context out
// from under a goroutine mid-run.
func (r *runState) context() context.Context {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ctx
}

// stop cancels the current run if there is one.
func (r *runState) stop() {
	r.mu.RLock()
	cancel := r.cancel
	r.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

// tunnelStatus is the one-line state a transport publishes for the panel.
//
// It has two writers that genuinely overlap: Restart clears it, and the run it
// is replacing sets it as that run starts and again when its control channel
// comes up. Restart cancels the old run and then sleeps two seconds before
// clearing, which is ample time for the cancelled run to finish its handshake
// and write "Connected" — so the two collide on a plain string field. Nothing
// reads it unless the sniffer is on, which is why it has gone unnoticed; a
// write against a write is a race whether or not anybody is reading.
type tunnelStatus struct {
	mu sync.RWMutex
	s  string
}

func (t *tunnelStatus) set(v string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.s = v
}

func (t *tunnelStatus) get() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.s
}
