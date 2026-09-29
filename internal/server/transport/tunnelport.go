package transport

import (
	"context"
	"net"
	"time"

	"github.com/backpack/backpack/internal/utils/acceptloop"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/sirupsen/logrus"
)

// tcpTunnelPort is the tunnel port of the transports that carry the tunnel over
// plain TCP — tcp (and stealth, which is tcp under Noise) and tcpmux. It binds
// the port with the tunnel's socket options, retrying rather than exiting when
// the port is taken, accepts for as long as the generation runs, applies the
// options to each accepted connection, and hands it to admit in a goroutine of
// its own. The two transports had a copy each; what differs between them —
// what admitting a connection means — is admit.
type tcpTunnelPort struct {
	ctx       context.Context
	addr      string
	rcvBuf    int
	sndBuf    int
	mss       int
	keepAlive time.Duration
	nodelay   bool
	listeners *listenerSet
	log       *logrus.Logger
	// preauth bounds the accepted connections still being judged. See
	// acceptloop.Gate.
	preauth *acceptloop.Gate

	// admit takes one accepted connection through whatever it has to pass
	// before it can be used. It runs in the connection's own goroutine, so a
	// peer that connects and then says nothing never delays the connections
	// behind it, and holds one of preauth's places until it is judged.
	admit func(net.Conn)
}

// serve holds the port until the generation ends.
func (p tcpTunnelPort) serve() {
	// Counted while this goroutine holds a listener, so a restart can wait for
	// the port rather than sleeping and hoping. See listeners.go.
	p.listeners.hold()
	defer p.listeners.release()

	// The tunnel's own port is not optional, so a failed bind here cannot be
	// skipped the way a forwarded port can — but it is no reason to exit
	// either. Waiting and trying again is what the two real causes call for: a
	// previous instance still shutting down, or the port in TIME_WAIT. Both
	// clear on their own. See bindfail.go.
	listener, ok := bindTunnelPort(p.ctx, p.log, p.addr, func() (net.Listener, error) {
		return network.ListenWithBuffers("tcp", p.addr, p.rcvBuf, p.sndBuf, p.mss, p.keepAlive, !p.nodelay)
	})
	if !ok {
		return
	}
	defer listener.Close()

	p.log.Infof("server started successfully, listening on address: %s", listener.Addr().String())

	go p.accept(listener)

	<-p.ctx.Done()
}

func (p tcpTunnelPort) accept(listener net.Listener) {
	var backoff acceptBackoff
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}
		conn, err := listener.Accept()
		if err != nil {
			p.log.Debugf("failed to accept tunnel connection on %s: %v", listener.Addr(), err)
			// Back off rather than retry instantly: a closed listener fails
			// immediately and forever, and `continue` would pin a core.
			if !backoff.Fail(p.ctx) {
				return
			}
			continue
		}
		backoff.OK()

		tcpConn, ok := conn.(*net.TCPConn)
		if !ok {
			p.log.Warnf("discarded non-TCP tunnel connection from %s", conn.RemoteAddr().String())
			conn.Close()
			continue
		}
		p.tune(tcpConn)

		// Held until admit has judged the connection: admit returns once it
		// has filed it or refused it, and what serves it afterwards runs
		// elsewhere.
		leave, ok := p.preauth.Admit(conn, p.log.Debugf)
		if !ok {
			continue
		}
		go func() {
			defer leave()
			p.admit(conn)
		}()
	}
}

// tune applies the tunnel's socket options to an accepted connection.
func (p tcpTunnelPort) tune(c *net.TCPConn) {
	if !p.nodelay {
		if err := c.SetNoDelay(false); err != nil {
			p.log.Warnf("failed to set TCP_NODELAY for %s: %v", c.RemoteAddr().String(), err)
		}
	}
	if err := c.SetKeepAlive(true); err != nil {
		p.log.Warnf("failed to enable TCP keep-alive for %s: %v", c.RemoteAddr().String(), err)
	}
	if err := c.SetKeepAlivePeriod(p.keepAlive); err != nil {
		p.log.Warnf("failed to set TCP keep-alive period for %s: %v", c.RemoteAddr().String(), err)
	}
}
