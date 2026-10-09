package transport

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
)

// portForwarder is the user-facing half of a stream transport on the Iran
// side: it binds every forwarded port — TCP, and UDP too when the tunnel
// forwards it — admits each connection under the tunnel's limits into the
// generation's local queue, tells the transport a connection is waiting, and
// gives back whatever is still queued when the generation ends.
//
// All six stream transports had their own copy of this — mapping parser,
// listener, accept loop — differing only in the socket options they set on a
// user's connection and in what they do once one is queued. Those two
// differences are the adapters (tune and queued); everything else, including
// the fixes that each copy needed separately (limits released on every path,
// the queue drained when the generation ends), is here once.
type portForwarder struct {
	ctx       context.Context
	ports     []string
	acceptUDP bool
	queue     chan LocalTCPConn
	limits    *limiter
	listeners *listenerSet
	log       *logrus.Logger

	// tune applies the transport's socket options to an accepted user
	// connection. Optional.
	tune func(*net.TCPConn)
	// queued runs once a connection — a TCP client or a new UDP flow — is
	// waiting in queue: the transport asks its client for a tunnel connection
	// or a stream to carry it. Optional.
	queued func()
}

// run binds every mapping and returns; the listeners live until ctx ends.
func (f portForwarder) run() {
	go drainOnEnd(f.ctx, f.queue, f.limits)
	eachForward(f.ports, f.log, func(localAddr, target string) {
		go f.listen(localAddr, target)
	})
}

// eachForward expands the port mappings and calls bind for every port they
// name, with its target. A mapping that cannot be read is one mapping, not a
// reason to end the process — under a unit that restarts every three seconds
// one typo used to become a crash loop — so it is reported and skipped, and
// the tunnel's other ports are unaffected.
func eachForward(ports []string, log *logrus.Logger, bind func(localAddr, target string)) {
	for _, portMapping := range ports {
		parts := strings.Split(portMapping, "=")
		if len(parts) > 2 {
			log.Errorf("ignoring the port mapping %q: it has more than one '='", portMapping)
			continue
		}
		// The listen side is an optional bind address and either one port or
		// a range; see expandListenSpec.
		listens, err := expandListenSpec(parts[0])
		if err != nil {
			log.Errorf("ignoring the port mapping %q: %v", portMapping, err)
			continue
		}

		var remoteAddr string
		if len(parts) == 2 {
			remoteAddr = strings.TrimSpace(parts[1])
		}

		for _, l := range listens {
			// With no target, each port forwards to the same port on the far side.
			target := remoteAddr
			if target == "" {
				target = l.port
			}
			bind(l.addr, target)
			if len(listens) > 1 {
				time.Sleep(1 * time.Millisecond) // for wide port ranges
			}
		}
	}
}

// listen serves one forwarded port until the generation ends.
func (f portForwarder) listen(localAddr, remoteAddr string) {
	// Counted so a restart can wait for the port to be free again.
	f.listeners.hold()
	defer f.listeners.release()

	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		// Not fatal: one port being taken must not stop the others.
		f.log.Error(bindFailure("forwarded port", localAddr, err))
		return
	}
	defer listener.Close()

	f.log.Infof("listener started successfully, listening on address: %s", listener.Addr().String())

	go f.accept(listener, remoteAddr)

	// The same port number, over UDP, when the tunnel forwards UDP. Its flows
	// travel through the tunnel exactly like TCP connections do.
	if f.acceptUDP {
		go startUDPForward(f.ctx, f.log, localAddr, remoteAddr, f.admitUDP)
	}

	<-f.ctx.Done()
}

// accept admits the users of one forwarded port into the queue.
func (f portForwarder) accept(listener net.Listener, remoteAddr string) {
	var backoff acceptBackoff
	for {
		select {
		case <-f.ctx.Done():
			return
		default:
		}
		conn, err := listener.Accept()
		if err != nil {
			f.log.Debugf("failed to accept connection on %s: %v", listener.Addr(), err)
			// A closed listener ends the loop; anything else is retried with
			// a pause, so a persistent error does not spin a core.
			if !backoff.Fail(f.ctx) {
				return
			}
			continue
		}
		backoff.OK()

		tcpConn, ok := conn.(*net.TCPConn)
		if !ok {
			f.log.Warnf("discarded non-TCP connection from %s", conn.RemoteAddr().String())
			conn.Close()
			continue
		}
		if f.tune != nil {
			f.tune(tcpConn)
		}

		// The connection cap is enforced here, where users arrive: refusing
		// before queueing costs nothing downstream.
		if !f.limits.acquire() {
			f.log.Warnf("connection limit reached, refusing %s", conn.RemoteAddr())
			conn.Close()
			continue
		}
		wrapped := f.limits.wrap(f.ctx, conn)

		select {
		case f.queue <- LocalTCPConn{conn: wrapped, remoteAddr: remoteAddr, timeCreated: time.Now().UnixMilli()}:
			f.log.Debugf("forwarded port: accepted a client from %s", tcpConn.RemoteAddr().String())
			if f.queued != nil {
				f.queued()
			}
		default:
			// The queue is full: the tunnel is not keeping up, and holding the
			// connection open longer only delays the user's retry.
			f.log.Warnf("forwarded port %s: the queue is full, dropping a client from %s",
				listener.Addr().String(), tcpConn.RemoteAddr().String())
			f.limits.release()
			conn.Close()
		}
	}
}

// admitUDP admits a new UDP flow exactly as accept admits a TCP connection:
// the same cap, the same queue, the same request for a way through.
func (f portForwarder) admitUDP(conn net.Conn, target string) bool {
	if !f.limits.acquire() {
		return false
	}
	select {
	case f.queue <- LocalTCPConn{conn: f.limits.wrap(f.ctx, conn), remoteAddr: target, timeCreated: time.Now().UnixMilli()}:
		if f.queued != nil {
			f.queued()
		}
		return true
	default:
		f.limits.release()
		return false
	}
}

// requestAlways is the queued adapter of the transports that carry one user
// per tunnel connection: every waiting user asks the client for one more.
func requestAlways(reqNewConn chan struct{}, log *logrus.Logger) func() {
	return func() {
		select {
		case reqNewConn <- struct{}{}:
		default:
			// Already enough requests outstanding to cover the queue.
			log.Warn("channel is full, cannot request a new connection")
		}
	}
}

// nodelayTune turns Nagle's algorithm back on for a user's connection when the
// tunnel is configured without nodelay; accepted sockets have it off.
func nodelayTune(nodelay bool, log *logrus.Logger) func(*net.TCPConn) {
	return func(c *net.TCPConn) {
		if nodelay {
			return
		}
		if err := c.SetNoDelay(false); err != nil {
			log.Warnf("failed to set TCP_NODELAY for %s: %v", c.RemoteAddr().String(), err)
		}
	}
}

// muxRequest is the queued adapter of the mux transports: a waiting user is
// one more stream, and a new session is asked for only once the live ones are
// carrying as many streams as they may.
func muxRequest(streams, sessions *int32, muxCon int, reqNewConn chan struct{}, log *logrus.Logger) func() {
	return func() {
		n := atomic.AddInt32(streams, 1)
		if n < atomic.LoadInt32(sessions)*int32(muxCon) {
			return
		}
		log.Tracef("stream counter: %v, session counter: %v", n, atomic.LoadInt32(sessions))
		select {
		case reqNewConn <- struct{}{}:
		default:
			log.Warn("failed to request new connection. channel is full")
		}
	}
}

func muxRequestForGeneration(ctx context.Context, mu *sync.Mutex, streams, sessions *int32, muxCon int, reqNewConn chan struct{}, log *logrus.Logger) func() {
	ask := muxRequest(streams, sessions, muxCon, reqNewConn, log)
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() == nil {
			ask()
		}
	}
}

// alwaysNodelay turns Nagle's algorithm off on a user's connection, for the
// transports that do so whatever the configuration says.
func alwaysNodelay(log *logrus.Logger) func(*net.TCPConn) {
	return func(c *net.TCPConn) {
		if err := c.SetNoDelay(true); err != nil {
			log.Warnf("failed to set TCP_NODELAY for %s: %v", c.RemoteAddr().String(), err)
		}
	}
}

// wsTune is the websocket transports' socket options for a user's connection.
func wsTune(c *net.TCPConn, nodelay bool, keepAlive time.Duration, log *logrus.Logger) {
	nodelayTune(nodelay, log)(c)
	if err := c.SetKeepAlive(true); err != nil {
		log.Warnf("failed to enable TCP keep-alive for %s: %v", c.RemoteAddr().String(), err)
	}
	if err := c.SetKeepAlivePeriod(keepAlive); err != nil {
		log.Warnf("failed to set TCP keep-alive period for %s: %v", c.RemoteAddr().String(), err)
	}
}
