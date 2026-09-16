package transport

import (
	"time"

	"github.com/sirupsen/logrus"
)

// pairingTimeout is how long an accepted client connection waits for a tunnel
// connection to carry it before it is given up on.
//
// Three seconds, which is what every transport has always used, written once
// here because it was written seven times as a bare 3000 and the number was not
// the part that differed.
//
// What differed was when it was consulted. The check sat at the top of the
// pairing loop and the loop then blocked on the tunnel channel, so it only ever
// ran when a tunnel connection arrived — which means it did not run in the one
// case it exists for. A pool that has run dry delivers nothing, and the client
// connection was held open with nobody waiting on it and nothing to time it
// out: the browser sat there until it gave up on its own, the slot it took
// against max_connections was never returned, and the socket stayed open for
// the life of the run. The timeout now runs on a timer, so it fires whether or
// not anything arrives.
const pairingTimeout = 3 * time.Second

// pairingWait returns how long is left of a connection's pairing timeout.
//
// Never zero or negative: a timer built from a non-positive duration fires
// immediately and forever, which turns a pairing loop into a spin. The caller
// has already taken the expired path by then; this is the floor that makes the
// timer safe if it has not.
func pairingWait(createdMillis int64) time.Duration {
	left := pairingTimeout - time.Duration(nowMillis()-createdMillis)*time.Millisecond
	if left <= 0 {
		return time.Millisecond
	}
	return left
}

// nowMillis is what every transport already measures connection age with.
func nowMillis() int64 { return time.Now().UnixMilli() }

// requeueLocal puts a connection back on the local queue for another attempt,
// or gives up on it when there is no room.
//
// The send must not block, and it was a bare `g.localChannel <- incomingConn`.
// That send is made from inside the goroutine that drains this very channel: in
// the mux transports there is one such goroutine per session, and the session
// making the send is one that has just failed, so it is frequently the only one
// running. A full channel is then a goroutine waiting for itself — the queue
// holds channel_size connections while the loop that empties it is parked on a
// send into it, and the tunnel stops for good with every socket still open and
// nothing in the log to say why.
//
// A connection that cannot be re-queued is closed and its slot returned, which
// is exactly what the accept path already does when it finds the same channel
// full. Dropping one client is the cost; the alternative is dropping all of
// them until somebody restarts the service.
func requeueLocal(ch chan LocalTCPConn, conn LocalTCPConn, lim *limiter, logger *logrus.Logger) bool {
	select {
	case ch <- conn:
		return true
	default:
		if conn.conn != nil {
			logger.Warnf("the local queue is full, dropping a client from %s", conn.conn.RemoteAddr())
			conn.conn.Close()
		}
		lim.release()
		return false
	}
}
