package server

import (
	"context"
	"net"
	"sync"
	"sync/atomic"

	"github.com/backpack/backpack/internal/plain/core"
	"github.com/backpack/backpack/internal/web"
	"github.com/sirupsen/logrus"
)

// TunnelChannel is a pooled websocket tunnel connection.
type TunnelChannel struct {
	conn *core.WSConn
	ping chan struct{}
	mu   *sync.Mutex
}

// LocalTCPConn is a user's connection waiting for a tunnel connection: a TCP
// connection, or on the mux and ws transports a UDP flow (udp, with conn nil).
type LocalTCPConn struct {
	conn        net.Conn
	udp         *LocalAcceptUDPConn
	remoteAddr  string
	timeCreated int64
}

// target is the address the kharej is sent for c, marked for a UDP flow.
func (c LocalTCPConn) target() string {
	if c.udp != nil {
		return core.UDPTarget(c.remoteAddr)
	}
	return c.remoteAddr
}

// drop gives up on c before it was carried.
func (c LocalTCPConn) drop() {
	if c.udp != nil {
		c.udp.release()
		return
	}
	c.conn.Close()
}

// relay carries c over tunnel until either side ends.
func (c LocalTCPConn) relay(ctx context.Context, proxyProtocol bool, tunnel net.Conn, logger *logrus.Logger, usage *web.Usage, sniffer bool) {
	if c.udp != nil {
		UDPConnectionHandler(ctx, c.udp, core.Counted(tunnel), logger, usage, c.udp.port(), sniffer, noCongestion)
		return
	}
	core.TCPConnectionHandler(ctx, proxyProtocol, c.conn, core.Counted(tunnel), logger, usage, c.conn.LocalAddr().(*net.TCPAddr).Port, sniffer)
}

// LocalAcceptUDPConn is a forwarded UDP flow.
type LocalAcceptUDPConn struct {
	timeCreated int64
	payload     chan []byte
	remoteAddr  string
	listener    *net.UDPConn
	clientAddr  *net.UDPAddr
	table       *udpTable
	// IsCongested is set from the flow's reader and read by the listener;
	// atomic for that, not a plain bool.
	IsCongested atomic.Bool
}

// netControl is a generation's control connection. Set by the handshake and
// read by the accept loop, so behind an atomic.
type netControl struct{ p atomic.Pointer[net.Conn] }

func (c *netControl) Set(conn net.Conn) { c.p.Store(&conn) }

func (c *netControl) Get() net.Conn {
	if p := c.p.Load(); p != nil {
		return *p
	}
	return nil
}

// wsControl is the same for a websocket control channel.
type wsControl struct {
	p atomic.Pointer[core.WSConn]
}

func (c *wsControl) Set(conn *core.WSConn) { c.p.Store(conn) }
func (c *wsControl) Get() *core.WSConn     { return c.p.Load() }
