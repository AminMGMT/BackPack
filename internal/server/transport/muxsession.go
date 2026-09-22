package transport

import (
	"context"
	"sync/atomic"

	"github.com/sirupsen/logrus"
	"github.com/xtaci/smux"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/web"
)

// What a mux transport does with a session, written once.
//
// `tcpmux`, `wsmux` and `kcp` all reach the same place by different roads: a
// smux session, over TCP, over a websocket, or over KCP. From there the three
// of them did exactly the same thing — take a waiting local connection, open a
// stream, announce the backend, relay — and they did it in three copies that
// were character-for-character identical apart from the receiver's type and two
// comments.
//
// That is the same shape of duplication pairloop.go was written for, and it has
// the same history. Every fault found in this loop had to be found three times:
//
//   - the pooled slot was not released when a connection timed out waiting to
//     be paired, so a tunnel with max_connections lost a slot to every timeout
//     until it refused everything;
//   - the mux slot was not given back when announcing the backend failed, so a
//     session that failed that way MuxCon times stopped taking connections at
//     all — the loop blocks at the top on a counter that is full, and the only
//     goroutine that empties it is the one that is blocked;
//   - a connection that could not be requeued was left counted, so the stream
//     counter drifted upward and the pool stopped asking for connections.
//
// Each was fixed once per copy. This is the one copy.
//
// # What is still per transport
//
// Everything above the session: how to listen, how to accept, how to get a
// smux session out of whatever arrived, and when to ask for another. Those are
// genuinely different. What happens once a session exists is not, and this is
// the whole of it.

// muxSession is one smux session and everything it needs from the transport
// that owns it.
//
// The counters are pointers rather than values because they belong to the
// transport and outlive any one session — a session that ends must leave them
// where the next one can see them.
type muxSession struct {
	ctx        context.Context
	local      chan LocalTCPConn
	usage      *web.Usage
	reqNewConn chan struct{}

	// muxCon is how many streams one session may carry at once.
	muxCon int
	// proxyProtocol and sniffer are the two per-connection settings the relay
	// takes; they are read from the config once rather than per stream.
	proxyProtocol bool
	sniffer       bool

	limits   *limiter
	log      *logrus.Logger
	streams  *int32
	sessions *int32
}

// run carries connections over one session until the session or the run ends.
func (m muxSession) run(session *smux.Session) {
	// The mux slot counter. Its capacity is the ceiling on streams in flight on
	// this session: the loop blocks at the top when it is full, and every path
	// out of a stream has to give one back or the session quietly stops taking
	// work.
	counter := make(chan struct{}, m.muxCon)
	defer session.Close()
	defer close(counter)

	for {
		// +1 for mux connection counter
		counter <- struct{}{}

		select {
		case <-m.ctx.Done():
			return

		case incomingConn := <-m.local:
			if nowMillis()-incomingConn.timeCreated > pairingTimeout.Milliseconds() {
				m.log.Debugf("timeouted local connection: %d ms", nowMillis()-incomingConn.timeCreated)
				incomingConn.conn.Close()

				// Free the slot this connection took on accept. It is otherwise
				// released only by the handler goroutine, which never runs for a
				// connection that timed out waiting to be paired — so a tunnel
				// with max_connections set loses a slot to every timeout and
				// eventually refuses everything.
				m.limits.release()

				atomic.AddInt32(m.streams, -1)
				<-counter
				continue
			}

			stream, err := session.OpenStream()
			if err != nil {
				m.failed(&incomingConn, err)
				return
			}

			// Send the target port over the tunnel connection
			if err := utils.SendBinaryString(stream, incomingConn.remoteAddr); err != nil {
				m.log.Tracef("failed to send address over stream: %v", err)
				// The stream is unusable and nothing else will close it.
				stream.Close()

				// Give back the mux slot this attempt took. It was not given
				// back, so a session that failed this way MuxCon times stopped
				// taking connections at all: the loop blocks at the top on a
				// counter that is full, and the only goroutine that empties it
				// is this one.
				<-counter

				// Back on the queue for another stream, without blocking — see
				// requeueLocal. A connection that goes back is still in flight
				// and stays counted; one there was no room for is counted out
				// here, because nothing downstream will ever do it.
				if !requeueLocal(m.local, incomingConn, m.limits, m.log) {
					atomic.AddInt32(m.streams, -1)
				}
				continue
			}

			// Handle data exchange between connections
			go func() {
				// Free the connection slot once the transfer ends, or the
				// limit would fill up permanently.
				defer m.limits.release()
				handlers.TCPConnectionHandler(m.ctx, m.proxyProtocol && !isUDPFlow(incomingConn.conn),
					incomingConn.conn, metrics.CountedConn(stream), m.log, m.usage,
					localForwardPort(incomingConn.conn), m.sniffer)
				atomic.AddInt32(m.streams, -1)
				<-counter // read signal from the channel
			}()
		}
	}
}

// failed retires a session that can no longer open streams, puts the connection
// it was holding back on the queue, and asks for a replacement.
func (m muxSession) failed(incomingConn *LocalTCPConn, err error) {
	m.log.Tracef("failed to handle session: %v", err)

	// decrease session value
	atomic.AddInt32(m.sessions, -1)

	// Back on the queue, without blocking. This runs on the session goroutine
	// that has just failed and is about to return, so there may be no other
	// goroutine left to drain the channel it is sending into. See requeueLocal.
	// A connection there was no room for is counted out, since nothing
	// downstream will do it.
	if !requeueLocal(m.local, *incomingConn, m.limits, m.log) {
		atomic.AddInt32(m.streams, -1)
	}

	// Attempt to request a new connection
	select {
	case m.reqNewConn <- struct{}{}:
	default:
		m.log.Warn("request new connection channel is full")
	}
}
