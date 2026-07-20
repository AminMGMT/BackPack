package transport

import (
	"net"
	"sync"

	"github.com/gorilla/websocket"
)

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
