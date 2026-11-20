package transport

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"
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

// refusalReason reads a server's explanation for turning a handshake down, if
// that is what the answer is.
//
// The server used to close the connection instead, which reaches the client as
// EOF — indistinguishable from a server too old to know the signal at all. That
// is how a mistyped token and a second client on one server both came back as
// "the server is running an older version", and operators upgraded servers that
// had nothing wrong with them.
func refusalReason(ack string, signal byte) (string, bool) {
	if signal != utils.SG_Refused {
		return "", false
	}
	switch ack {
	case utils.RefusedBadToken:
		return "the server rejected the token — the two ends do not have the same one", true
	case utils.RefusedAuth:
		return "the server could not check this client's proof of the token — the two ends do not have the same one", true
	case utils.RefusedPeerAuth:
		return "the server requires v2.0.0 at this end and this client offered the older handshake. " +
			"It does so only when the server never answered the new one, so look at the path, or at " +
			"whether something in between is interfering with the handshake", true
	case utils.RefusedTooOld:
		return "the server no longer speaks this client's handshake (older than v1.7.0) — upgrade this client", true
	case utils.RefusedInUse:
		return "the server already has a control channel from somebody else. Two clients " +
			"dialling one server with the same token do this, and so does an old service " +
			"left running beside its replacement", true
	default:
		return "the server refused the handshake: " + ack, true
	}
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
	return utils.SendBinaryTransportStringWithin(conn, nonce, utils.SG_Pool, 10*time.Second)
}

// restartingRefusal reports whether a control claim was answered "come back in
// a moment" — the server took the token and is restarting its run to adopt this
// client — rather than refused for cause.
func restartingRefusal(ack string, signal byte) bool {
	return signal == utils.SG_Refused && ack == utils.RefusedRestarting
}

// beatClock learns how often the server's heartbeat actually arrives, so a
// dead server is noticed in a few beats rather than in a keepalive and a half.
//
// The control deadline was one and a half keepalives — 112 seconds at the
// default — because the client had no other idea how long silence could
// legitimately last. On TCP that rarely mattered: a crashed server's kernel
// answers with a reset. Over KCP and QUIC there is no kernel to answer, and a
// server that crashed or rebooted cost the tunnel the full 112 seconds,
// measured, on every one of those transports.
//
// The server says how often it beats by beating. Once three gaps have been
// seen, the deadline is three of the longest gap — two lost beats in a row
// tolerated — floored so a quick server cannot make a lossy path look dead,
// and never longer than the keepalive rule it replaces. Against a server that
// beats every forty seconds that is 120, above the old rule, so nothing
// changes; against one that beats every ten it is thirty.
//
// It is per control channel: a new one starts with a new clock, so a server
// replaced by an older, slower one is not held to the faster one's rhythm.
// Only the reader goroutine touches it.
//
// Learning takes three gaps, and a server that dies before they arrive used to
// cost the whole fallback. From v1.8.2 a server opens each channel with a
// warm-up of quick beats, the first a tenth of a second in; no older server
// beats sooner than a second after the channel opens (one second is the
// shortest heartbeat it accepts, and its first beat is one interval in). So a
// first beat inside warmupEvidence is proof of a server that keeps a fast
// rhythm, and the clock trusts livenessFloor from then on rather than waiting
// to learn it — which is what closes the gap for a crash in the first second
// or two of a connection.
type beatClock struct {
	opened time.Time
	last   time.Time
	gaps   []time.Duration
	quick  bool // the first beat came within warmupEvidence of opening
}

// warmupEvidence is how soon after the channel opens a first beat has to
// arrive to prove the server warms up. Under the one second no older server
// can beat in, with room for a slow path.
const warmupEvidence = 700 * time.Millisecond

// newBeatClock starts the clock of a control channel that opened at now.
func newBeatClock(now time.Time) *beatClock { return &beatClock{opened: now} }

// beatHistory is how many recent gaps are kept; the longest of them decides.
const beatHistory = 4

// beatsToLearn is how many gaps have to be seen before the clock is trusted.
const beatsToLearn = 3

// livenessFloor is the shortest deadline the clock will ever set.
const livenessFloor = 15 * time.Second

func (b *beatClock) beat(now time.Time) {
	if b.last.IsZero() && !b.opened.IsZero() && now.Sub(b.opened) < warmupEvidence {
		b.quick = true
	}
	if !b.last.IsZero() {
		b.gaps = append(b.gaps, now.Sub(b.last))
		if len(b.gaps) > beatHistory {
			b.gaps = b.gaps[1:]
		}
	}
	b.last = now
}

// deadline is how long the next read may wait for anything from the server.
func (b *beatClock) deadline(keepAlive time.Duration) time.Duration {
	base := controlDeadline(keepAlive)
	if len(b.gaps) < beatsToLearn {
		if b.quick {
			return min(livenessFloor, base)
		}
		return base
	}
	var worst time.Duration
	for _, g := range b.gaps {
		worst = max(worst, g)
	}
	d := max(3*worst, livenessFloor)
	return min(d, base)
}

// explain says, when a control read timed out, what the silence most likely
// means — or nothing, for any other error.
//
// A client that gives up after one and a half keepalives on a server that
// heartbeats less often than that reconnects over and over, and each
// reconnect looked like the one before: a tunnel dropping every half minute
// with nothing in either log to say why (issue #45). The client cannot tell a
// slow server from a dead one, but it can tell whether a heartbeat has ever
// arrived on this channel — and if none has, a heartbeat setting longer than
// its own patience is the likeliest cause, and the one worth naming.
func (b *beatClock) explain(err error, keepAlive time.Duration) string {
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		return ""
	}
	window := b.deadline(keepAlive).Round(time.Second)
	if !b.last.IsZero() {
		return fmt.Sprintf("nothing heard from the server for %s after it had been heartbeating — "+
			"the server has gone, or the path is dropping packets. Reconnecting.", window)
	}
	return fmt.Sprintf("no heartbeat has arrived on this control channel in %s. If the server's "+
		"heartbeat setting is longer than that, this client gives up before the first one and "+
		"reconnects every time — raise keepalive_period on this side to at least two thirds of "+
		"the server's heartbeat, or upgrade the server (from v1.8.2 it heartbeats every 10 "+
		"seconds whatever its setting). Reconnecting.", window)
}
