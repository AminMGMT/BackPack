package transport

import (
	"net"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"

	"github.com/sirupsen/logrus"
)

// The client half of the control handshake.
//
// A pool connection used to prove nothing: the server accepted it because its
// source address matched the control channel's. The client now asks for a nonce
// instead, and presents it on every pool connection — see network.PoolNonce for
// why the address was the wrong thing to judge by.
//
// Asking is a new signal rather than a flag inside the old one, which makes the
// negotiation trivial in one direction and cheap in the other. A server that
// already speaks it answers with the nonce. A server that does not never
// recognises the signal and closes the connection, and the client notices that
// and drops back to the old handshake. The cost of meeting an old server is a
// couple of failed connection attempts.

// controlAckTimeout is how long to wait for the server's answer to the
// handshake.
//
// It was two seconds, which is the kind of number that works everywhere it is
// tested and then does not: it has to cover a round trip plus whatever the
// server takes to answer, and on a long or lossy path — which is the ordinary
// case for this tunnel, not the exceptional one — a retransmitted SYN alone can
// eat most of it. There is nothing to gain from failing fast here, because
// failing means backing off and dialling again, which costs far more than
// waiting a few seconds longer would have.
const controlAckTimeout = 15 * time.Second

// The bounds on how long a client waits for any word from the server before
// deciding the tunnel is dead. See controlDeadline.
const (
	controlIdleFallback = 120 * time.Second
	controlIdleFloor    = 30 * time.Second
)

// controlDeadline is how long to wait for any word from the server on the
// control channel before treating the tunnel as gone.
//
// Without one, a stream transport is left relying on TCP to notice, and TCP is
// in no hurry: with the default keepalive_period of 75s the kernel sends nine
// probes 75s apart after 15s of silence, so a tunnel that died seconds after it
// came up is reported healthy for another eleven and a half minutes. The
// watchdog cannot cover for that either — the socket stays ESTABLISHED for the
// whole of it, which is the only thing the watchdog can see.
//
// The server heartbeats every heartbeat seconds and the tuner writes
// keepalive_period as twice that, so keepAlive is worth two heartbeat intervals
// here. Half again on top is what survives a single lost heartbeat: at two
// intervals exactly, one dropped heartbeat on a lossy link is enough to tear
// down a tunnel that is alive, which is the failure worth avoiding most —
// RecommendKeepAlive is built around the same rule. On the defaults this
// notices a dead tunnel in under two minutes instead of eleven.
//
// The floor is only there to stop a hand-written keepalive_period of a second
// or two from turning into a restart loop.
func controlDeadline(keepAlive time.Duration) time.Duration {
	if keepAlive <= 0 {
		return controlIdleFallback
	}
	if d := keepAlive + keepAlive/2; d > controlIdleFloor {
		return d
	}
	return controlIdleFloor
}

// legacyMissThreshold is how many unanswered v2 handshakes it takes to conclude
// the server does not speak one.
//
// One is not enough, and assuming it was is what made a healthy tunnel worse: a
// server that had already answered a v2 handshake minutes earlier was declared
// old on a single EOF, when all the EOF meant was that the path was still down
// from the disconnect being reconnected after. An old server refuses every
// time, so asking twice separates the two at the cost of one extra attempt in
// the case that is going away.
const legacyMissThreshold = 2

// legacyProbe decides which handshake to ask for, and keeps what the server has
// actually proved about itself separate from what a failure might suggest.
type legacyProbe struct {
	// legacy is set once the server is believed not to understand SG_ChanV2.
	legacy atomic.Bool
	// answered records that this server has completed a v2 handshake at least
	// once. It is proof, and it outranks any later failure: a server does not
	// become old.
	answered atomic.Bool
	// misses counts consecutive unanswered v2 attempts.
	misses atomic.Int32
	// warned keeps the explanation to once per process however often the
	// probe is re-armed.
	warned atomic.Bool
}

// signal picks which handshake to ask for.
func (p *legacyProbe) signal() byte {
	if p.legacy.Load() {
		return utils.SG_Chan
	}
	return utils.SG_ChanV2
}

// ack records the server's answer to a handshake that succeeded.
//
// A v2 answer clears the fallback as well as arming the proof, so a client that
// guessed wrong — or that has been moved to a server which has since been
// upgraded — goes back to the nonce instead of staying degraded for the life of
// the process.
func (p *legacyProbe) ack(ackSignal byte) {
	p.misses.Store(0)
	if ackSignal == utils.SG_ChanV2 {
		p.answered.Store(true)
		p.legacy.Store(false)
	}
}

// miss records a v2 handshake that went unanswered, and falls back once there
// is enough of them to mean something.
//
// It is called only for a non-timeout failure. An older server rejects the
// signal it does not know by closing the connection, which surfaces as a read
// error immediately; a filtered or broken path times out instead. Treating a
// timeout as evidence of an old server would give up the nonce over a momentary
// outage. A close is not proof on its own either, which is what the threshold
// and the proof below are for.
func (p *legacyProbe) miss(logger *logrus.Logger, sent byte) {
	if sent != utils.SG_ChanV2 {
		return
	}
	// This server has answered a v2 handshake before. Whatever just closed the
	// connection, it was not a server that does not know the signal.
	if p.answered.Load() {
		return
	}
	if p.misses.Add(1) < legacyMissThreshold {
		return
	}
	if p.legacy.Swap(true) || p.warned.Swap(true) {
		return
	}
	// What was seen, then what it most likely means, then what to check. The
	// previous wording stated the conclusion as fact — "so it is running an older
	// version" — and a user acted on it, upgrading a server that was fine while
	// the real fault was the path. A message is allowed to draw a conclusion; it
	// is not allowed to hide that it drew one.
	logger.Warnf("the server closed %d handshake attempts without answering the current one. "+
		"That is what an older server does, so this client is falling back to the previous "+
		"handshake, in which the server identifies pool connections by their source address — "+
		"which fails if this machine dials out from more than one address. If the server is "+
		"in fact up to date, look at the path instead: a connection closed in transit looks "+
		"the same from here.", legacyMissThreshold)
}

// reset re-arms the probe for a fresh run of the tunnel.
//
// The fallback used to be a one-way latch that nothing cleared, so a single
// dropped connection cost the nonce until the process was restarted — on a
// server that spoke v2 perfectly well. A run that is starting over has no
// reason to carry that verdict, and re-asking costs at most a couple of
// attempts against a server that really is old.
func (p *legacyProbe) reset() {
	p.legacy.Store(false)
	p.misses.Store(0)
}

// decodeControlAck reads the server's answer, returning the token to check it
// by, the nonce for pool connections, and the mux version to run this tunnel
// at. A legacy server sends only the token, so the nonce is empty and the
// version is 0 — nothing to apply, and each end keeps its own configuration.
//
// The server's version is used as given, even against this client's own
// mux_version. That is deliberate, and it is what makes the whole thing safe:
// smux has no negotiation of its own and tears down any session whose two ends
// disagree, so exactly one side has to decide. Two ends each honouring their
// own file is precisely how they end up mismatched.
func decodeControlAck(ack string, signal byte) (token, nonce string, muxVersion int) {
	if signal != utils.SG_ChanV2 {
		return ack, "", 0
	}
	return network.DecodeControlAck(ack)
}

// announcePoolConn tells the server what a freshly dialled pool connection is,
// so it can be admitted on the strength of the nonce rather than its source
// address. Against a legacy server there is no nonce and nothing is sent, which
// is exactly the old behaviour.
func announcePoolConn(conn net.Conn, nonce string) error {
	if nonce == "" {
		return nil
	}
	return utils.SendBinaryTransportString(conn, nonce, utils.SG_Pool)
}
