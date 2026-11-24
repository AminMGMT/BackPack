package transport

import (
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/sirupsen/logrus"
)

// The client half of the plain transports' peer handshake (see
// network/peerauth.go and docs/adr/0006-plain-transports-prove-both-ends.md).

// controlReply is the server's last answer to a control claim.
type controlReply struct {
	ack    string
	signal byte
	// session is the peer handshake's; nil when the claim was made on the
	// handshake before v2.0.0 (an older server).
	session *network.PeerSession
}

// errServerUnproven is a server that answered the new handshake without
// proving it holds the token.
var errServerUnproven = errors.New("the server answered but could not prove it holds the tunnel token — " +
	"the two ends have different tokens, or something on the path is posing as the Iran server")

// claimControl makes a control-channel claim on conn and returns the server's
// answer. legacy is this transport's signal for the handshake before v2.0.0,
// offered only while the guard allows it and the server has twice closed the
// new one unanswered, which is what an older server does.
//
// A refusal is returned as an answer, not an error, so the caller reports it
// the way it always has.
func claimControl(conn net.Conn, guard *network.PeerGuard, token, label string, legacy byte, log *logrus.Logger) (controlReply, error) {
	if guard.UseLegacy() {
		if err := utils.SendBinaryTransportStringWithin(conn, token, legacy, 10*time.Second); err != nil {
			return controlReply{}, fmt.Errorf("failed to send security token: %w", err)
		}
		msg, sig, err := readControl(conn)
		return controlReply{ack: msg, signal: sig}, err
	}

	hello, err := guard.Key().NewPeerHello(label)
	if err != nil {
		return controlReply{}, err
	}
	if err := utils.SendBinaryTransportStringWithin(conn, hello.Encode(), utils.SG_ChanAuth, 10*time.Second); err != nil {
		return controlReply{}, fmt.Errorf("failed to send the handshake: %w", err)
	}
	msg, sig, err := readControl(conn)
	// An older server does not know SG_ChanAuth and closes the connection;
	// over udp's control port it judges the hello as a token and refuses it
	// as one. A server on v2.0.0 answers neither way.
	closed := err != nil && !isTimeout(err)
	if closed || (err == nil && sig == utils.SG_Refused && msg == utils.RefusedBadToken) {
		if guard.Missed() {
			log.Warnf("the server closed %d handshake attempts without answering the one v2.0.0 uses. "+
				"That is what a server on an older version does, so this client falls back to the previous "+
				"handshake, in which the tunnel token crosses the wire as it is. Upgrade the Iran server: once "+
				"it has answered the new way, this tunnel stops offering the old one. If the server is in fact "+
				"up to date, look at the path instead — a connection closed in transit looks the same from here.",
				network.PeerAuthMissThreshold)
		}
		if err == nil {
			err = errors.New("the server refused the new handshake as a token, as an older server does")
		}
		return controlReply{}, err
	}
	if err != nil || sig == utils.SG_Refused {
		return controlReply{ack: msg, signal: sig}, err
	}
	if sig != utils.SG_ChanAuth {
		return controlReply{}, fmt.Errorf("unexpected answer %d to the handshake", sig)
	}
	reply, ok := network.DecodePeerReply(msg)
	if !ok {
		return controlReply{}, errServerUnproven
	}
	sess, ok := guard.Key().CheckReply(hello, reply, label)
	if !ok {
		return controlReply{}, errServerUnproven
	}
	if err := utils.SendBinaryTransportStringWithin(conn, sess.Confirm(), utils.SG_ChanAuth, 10*time.Second); err != nil {
		return controlReply{}, fmt.Errorf("failed to finish the handshake: %w", err)
	}
	msg, sig, err = readControl(conn)
	if err != nil {
		return controlReply{}, err
	}
	if sig == utils.SG_ChanAuth {
		guard.Proved()
		return controlReply{ack: msg, signal: sig, session: sess}, nil
	}
	return controlReply{ack: msg, signal: sig}, nil
}

// readControl reads one framed answer under the control-ack deadline and
// clears the deadline afterwards.
func readControl(conn net.Conn) (string, byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(controlAckTimeout)); err != nil {
		return "", 0, err
	}
	msg, sig, err := utils.ReceiveBinaryTransportString(conn)
	if err != nil {
		return "", 0, err
	}
	conn.SetReadDeadline(time.Time{})
	return msg, sig, nil
}

// peerSession is the current control channel's handshake session, which
// every destination the server sends must open under. nil means the server is
// on the handshake before v2.0.0, whose destinations come as they are.
type peerSession struct {
	p atomic.Pointer[network.PeerSession]
}

func (s *peerSession) Set(v *network.PeerSession) { s.p.Store(v) }
func (s *peerSession) Get() *network.PeerSession  { return s.p.Load() }
func (s *peerSession) Clear()                     { s.p.Store(nil) }

var legacyServerWarned atomic.Bool

// warnLegacyServer explains, once per process, what talking to an older
// server costs.
func warnLegacyServer(log *logrus.Logger) {
	if legacyServerWarned.Swap(true) {
		return
	}
	log.Warn("connected on the handshake from before v2.0.0: the server is older. The tunnel token crossed " +
		"the wire as it is, and something on the path could pose as the server and choose where this machine " +
		"dials. Upgrade the Iran server.")
}

// The plain ws transports carry the handshake in the upgrade; see the server's
// wsPeerAuth. Over wss, and with simple_auth, the TLS-bound proofs (or the
// trusted proxy) do this job and the claim goes as it always has.
