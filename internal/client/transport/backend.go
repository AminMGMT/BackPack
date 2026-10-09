package transport

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/xtaci/smux"
)

// The last hop: from the tunnel to the service a user actually asked for, on
// this machine or one it can reach.
//
// Every transport ends here, and each had its own copy of it — pick a healthy
// backend, choose the socket buffers, dial, report a failure the way
// localdial.go words it, relay — with the multiplexed ones also carrying a copy
// of the loop that takes streams off a session. They differed only in their
// socket options, which are backendOpts.

// backendOpts is how a transport's users reach the local service.
type backendOpts struct {
	dialTimeout time.Duration
	keepAlive   time.Duration
	// rcvBuf and sndBuf are the socket buffers for a backend on another
	// machine; a loopback backend always gets 32 KB. Zero leaves the kernel's.
	rcvBuf int
	sndBuf int
	mss    int
	// sniffer records per-port usage for the panel.
	sniffer bool
}

// loopbackBuffer is the socket buffer for a backend on this machine, where a
// large buffer only adds latency.
const loopbackBuffer = 32 * 1024

// isLoopback reports whether addr is on this machine: any loopback address, v4
// or v6, or localhost.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// dialBackend dials addr — after picking a healthy one when several backends
// are configured — and returns the connection, or nil when the dial failed,
// which has then already been reported.
func (l *lifecycle) dialBackend(addr string, o backendOpts) net.Conn {
	addr = backends.pick(addr)
	rcv, snd := o.rcvBuf, o.sndBuf
	if isLoopback(addr) {
		rcv, snd = loopbackBuffer, loopbackBuffer
	}
	conn, err := network.TcpDialer(l.state.Ctx(), addr, o.dialTimeout, o.keepAlive, true, 1, rcv, snd, o.mss)
	if err != nil {
		localDial.Report(l.logger, addr, err)
		return nil
	}
	// The last hop worked, so any run of failures recorded for the panel ends
	// here. See localdial.go.
	ReportLocalDialOK()
	l.logger.Debugf("connected to local address %s successfully", addr)
	return conn
}

// relay dials the backend for one user already resolved to addr and port, and
// carries the user's connection to it until either side closes.
func (l *lifecycle) relay(user net.Conn, addr string, port int, o backendOpts) {
	backend := l.dialBackend(addr, o)
	if backend == nil {
		user.Close()
		return
	}
	handlers.TCPConnectionHandler(l.state.Ctx(), false, metrics.CountedConn(user), backend, l.logger, l.state.Usage(), port, o.sniffer)
}

// relayStream serves one user arriving on a stream that names its own target:
// a UDP flow is handed to the UDP forwarder, anything else is resolved and
// relayed.
func (l *lifecycle) relayStream(stream net.Conn, target string, o backendOpts) {
	if dialForwardedUDP(l.state.Ctx(), o.dialTimeout, stream, target, l.logger, l.state.Usage(), o.sniffer) {
		return
	}
	port, addr, err := network.ResolveRemoteAddr(target)
	if err != nil {
		l.logger.Infof("failed to resolve remote port: %v", err)
		stream.Close()
		return
	}
	l.relay(stream, addr, int(port), o)
}

// serveSession takes the streams off one mux session until it ends, each one a
// user: the target it names is read in the stream's own goroutine, so a stream
// that is slow to name one never holds up the streams behind it.
func (l *lifecycle) serveSession(session *smux.Session, from net.Addr, o backendOpts) {
	ctx := l.state.Ctx()
	// The session is the generation's: when the generation ends the session
	// goes with it, and AcceptStream below returns. Waiting for the peer to
	// notice instead is waiting for ever over KCP, where nothing tells the
	// socket the far side has gone.
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer func() {
		stop()
		// However the loop ended, the session ends with it — including when
		// the generation was already over before the first AcceptStream.
		session.Close()
	}()
	for ctx.Err() == nil {
		stream, err := session.AcceptStream()
		if err != nil {
			l.logger.Trace("session is closed: ", err)
			return
		}
		l.state.Go(func() {
			_ = stream.SetReadDeadline(time.Now().Add(controlAckTimeout))
			target, err := utils.ReceiveBinaryString(stream)
			if err != nil {
				l.logger.Errorf("unable to get port from stream connection %s: %v", from, err)
				stream.Close()
				return
			}
			_ = stream.SetReadDeadline(time.Time{})
			l.relayStream(stream, target, o)
		})
	}
}
