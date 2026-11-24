package server

import (
	"net"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// TunnelChannel is a pooled websocket tunnel connection.
type TunnelChannel struct {
	conn *websocket.Conn
	ping chan struct{}
	mu   *sync.Mutex
}

// LocalTCPConn is a user's connection waiting for a tunnel connection.
type LocalTCPConn struct {
	conn        net.Conn
	remoteAddr  string
	timeCreated int64
}

// LocalAcceptUDPConn is a forwarded UDP flow over tcp.
type LocalAcceptUDPConn struct {
	timeCreated int64
	payload     chan []byte
	remoteAddr  string
	listener    *net.UDPConn
	clientAddr  *net.UDPAddr
	// IsCongested is set from the flow's reader and read by the listener;
	// atomic for that, which Backhaul's plain bool was not.
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
	p atomic.Pointer[websocket.Conn]
}

func (c *wsControl) Set(conn *websocket.Conn) { c.p.Store(conn) }
func (c *wsControl) Get() *websocket.Conn     { return c.p.Load() }
