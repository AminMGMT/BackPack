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
// and drops back to the old handshake for the rest of the process. The cost of
// meeting an old server is one failed connection attempt, once.

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

// controlSignal picks which handshake to ask for.
func controlSignal(legacy *atomic.Bool) byte {
	if legacy.Load() {
		return utils.SG_Chan
	}
	return utils.SG_ChanV2
}

// noteLegacyServer records that a v2 handshake went unanswered, so the next
// attempt asks for the old one.
//
// It is called only for a non-timeout failure. An older server rejects the
// signal it does not know by closing the connection, which surfaces as a read
// error immediately; a filtered or broken path times out instead. Treating a
// timeout as evidence of an old server would give up the nonce for the life of
// the process over a momentary outage.
func noteLegacyServer(logger *logrus.Logger, legacy *atomic.Bool, sent byte) {
	if sent != utils.SG_ChanV2 || legacy.Swap(true) {
		return
	}
	logger.Warn("the server did not answer the current handshake, so it is running an older version. Falling back to the previous one, in which the server has to identify this client's pool connections by their source address — which fails if this machine dials out from more than one address. Upgrade the server to remove that limitation.")
}

// decodeControlAck reads the server's answer, returning the token to check it
// by and the nonce for pool connections (empty from a legacy server).
func decodeControlAck(ack string, signal byte) (token, nonce string) {
	if signal != utils.SG_ChanV2 {
		return ack, ""
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
