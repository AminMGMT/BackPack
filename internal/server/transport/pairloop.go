package transport

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"
)

// The half of a transport's lifecycle that has actually leaked, written once.
//
// Seven transports on this side repeat accept → pair → admit → relay → release
// by copy. Most of that is genuinely different per transport — how it listens,
// how it frames, what a "connection" even is — but the pairing step is not: it
// is the same state machine every time, and the copies have already proved it
// by drifting.
//
//   - The pooled-slot release on a pairing timeout was missing from four of the
//     seven. A tunnel with max_connections lost a slot to every timeout until
//     it refused everything.
//   - The timeout itself could not fire in the one case it exists for. The age
//     was checked once and then the select blocked, so on a pool that had run
//     dry nothing ever woke up to time the connection out.
//   - The teardown path — a run ending while a connection sits here unpaired —
//     leaked one socket and one slot per parked connection on every restart.
//
// Each of those was found and fixed once per copy, on three separate
// occasions. This is the shape they were all supposed to have.
//
// # What is deliberately still per transport
//
// Announcing the backend address (a length-prefixed string, a websocket text
// frame, a smux stream write), discarding an unusable tunnel connection, and
// the relay itself. Those differ for real reasons. Everything above them —
// when to give up, who closes what, and who gives the slot back — does not.
//
// # Why this and not the whole skeleton
//
// idea.md §1.4 asks for a skeleton owning the entire lifecycle and says, in the
// line it calls the important one in the document, that it must not be
// attempted against this package's test coverage. That judgement still holds.
// This is the part of it that pays for itself now: it removes the copies that
// have leaked, it is bounded enough to read in one sitting, and every transport
// it touches is covered end to end by the transport matrix.

// pairing is one local connection waiting for a tunnel connection to carry it.
//
// It is generic over the tunnel connection because that is the only thing that
// genuinely varies: a net.Conn, a smux stream, a quic stream, a websocket
// channel. The state machine is identical for all of them.
type pairing[T any] struct {
	ctx    context.Context
	local  LocalTCPConn
	tunnel <-chan T
	limits *limiter
	log    *logrus.Logger

	// announce tells the far end which backend this connection is for. A
	// non-nil error means the tunnel connection is unusable.
	announce func(conn T, remoteAddr string) error

	// discard drops a tunnel connection that could not be announced on.
	// Nothing else will close it.
	discard func(conn T)

	// relay hands the pair to the data path. It is responsible for releasing
	// the connection slot when the transfer ends — that release happens when
	// the transfer does, which is not something this loop can wait for.
	relay func(conn T, local LocalTCPConn)

	// request asks the client for another tunnel connection, so a pool that
	// has run dry refills. Optional: the transports that keep the pool topped
	// up elsewhere leave it nil.
	request func()
}

// run waits for a tunnel connection and pairs it, or gives up.
//
// It returns when the connection has been handed to the data path, timed out,
// or the run ended. In every one of those cases exactly one of two things has
// happened to the connection slot: it was released here, or it was handed to
// relay, which releases it. There is no path out of this function that leaves
// it held.
func (p pairing[T]) run() {
	for {
		// The age is checked against a timer rather than only on entry. The
		// check-then-block shape is what made the timeout unable to fire in the
		// one case it exists for: with no tunnel connection coming, nothing
		// ever woke up to notice.
		timer := time.NewTimer(pairingWait(p.local.timeCreated))

		select {
		case <-p.ctx.Done():
			timer.Stop()
			// The run is going away and this connection never reached a
			// handler, so nothing else will ever close it or give its slot
			// back. Every restart used to leak one of each per parked
			// connection.
			p.abandon()
			return

		case <-timer.C:
			p.log.Debugf("timeouted local connection: %d ms", nowMillis()-p.local.timeCreated)
			p.abandon()
			return

		case conn := <-p.tunnel:
			timer.Stop()
			if err := p.announce(conn, p.local.remoteAddr); err != nil {
				p.log.Tracef("failed to send the address over the tunnel connection: %v", err)
				p.discard(conn)
				// That tunnel connection is gone; ask for another and wait
				// again. The local connection is still good and still counted.
				p.askForAnother()
				continue
			}
			p.relay(conn, p.local)
			return
		}
	}
}

// abandon closes the local connection and returns its slot. It is the only
// place in this file that does either, which is what makes "was the slot
// released" a question with one answer rather than seven.
func (p pairing[T]) abandon() {
	p.local.conn.Close()
	p.limits.release()
}

func (p pairing[T]) askForAnother() {
	if p.request != nil {
		p.request()
	}
}

// expired reports whether a local connection has been waiting too long to be
// worth pairing at all. Checked before a pairing is started, so a connection
// that sat in the queue past its deadline is not given a fresh timer.
func expired(local LocalTCPConn) bool {
	return nowMillis()-local.timeCreated > pairingTimeout.Milliseconds()
}

// drop is what a transport does with a local connection that is already too
// old: close it and give the slot back. Named so the three call sites cannot
// disagree about whether the release belongs there.
func drop(local LocalTCPConn, limits *limiter, log *logrus.Logger) {
	log.Debugf("timeouted local connection: %d ms", nowMillis()-local.timeCreated)
	local.conn.Close()
	limits.release()
}
