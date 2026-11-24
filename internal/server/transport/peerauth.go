package transport

import (
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/sirupsen/logrus"
)

// The server half of the plain transports' peer handshake (see
// network/peerauth.go and docs/adr/0006-plain-transports-prove-both-ends.md).

// admitAuthClaim completes the handshake a client opened with SG_ChanAuth on a
// stream (tcp, tcpmux, udp's control port), answering on conn. It returns the
// session on success; on failure the client has been told why and conn is
// closed.
func admitAuthClaim(conn net.Conn, guard *network.PeerGuard, label, hello string, log *logrus.Logger) (*network.PeerSession, bool) {
	h, ok := network.DecodePeerHello(hello)
	if !ok || !guard.Key().VerifyHello(h, label) {
		log.Warnf("a control channel claim from %s did not prove the token — telling it so", conn.RemoteAddr())
		refuseControl(conn, utils.RefusedAuth)
		return nil, false
	}
	reply, sess, err := guard.Key().Reply(h, label)
	if err != nil {
		log.Errorf("could not answer the control handshake: %v", err)
		conn.Close()
		return nil, false
	}
	if err := utils.SendBinaryTransportStringWithin(conn, reply.Encode(), utils.SG_ChanAuth, 10*time.Second); err != nil {
		conn.Close()
		return nil, false
	}
	ann, err := readAnnouncement(conn)
	if err != nil || ann.signal != utils.SG_ChanAuth || !sess.CheckConfirm(ann.payload) {
		log.Warnf("a control channel claim from %s did not finish the handshake", conn.RemoteAddr())
		refuseControl(conn, utils.RefusedAuth)
		return nil, false
	}
	guard.Proved()
	return sess, true
}

// authAck is the server's last word on a completed SG_ChanAuth handshake: the
// mux version this run uses, and nothing else. The pool tag is derived from
// the session on both ends, so it never crosses the wire.
func authAck(muxVersion int) string { return strconv.Itoa(muxVersion) }

// refuseLegacyClaim turns away a client on the handshake before v2.0.0 when
// this end no longer accepts it, and warns when it still does. tooOld is a
// client from before v1.7.0 (tcp and tcpmux's SG_Chan), which v2 no longer
// speaks at all. It reports whether the claim was refused (conn is then
// closed).
func refuseLegacyClaim(conn net.Conn, guard *network.PeerGuard, tooOld bool, log *logrus.Logger) bool {
	if tooOld {
		log.Warnf("a client older than v1.7.0 at %s was refused: v2 no longer speaks its handshake — upgrade it", conn.RemoteAddr())
		refuseControl(conn, utils.RefusedTooOld)
		return true
	}
	if guard.Strict() {
		log.Warnf("a client at %s offered the handshake from before v2.0.0 and was refused: %s",
			conn.RemoteAddr(), strictWhy(guard))
		refuseControl(conn, utils.RefusedPeerAuth)
		return true
	}
	warnLegacyPeer(log, "client")
	return false
}

func strictWhy(guard *network.PeerGuard) string {
	if p := guard.PinPath(); p != "" {
		return "this tunnel requires v2.0.0 at the other end (peer_auth = \"required\", or the other end proved " +
			"itself the new way before — remembered in " + p + "). Upgrade the other end; if it really was " +
			"moved back to an older version on purpose, delete that file and restart"
	}
	return "this tunnel requires v2.0.0 at the other end (peer_auth = \"required\"). Upgrade the other end"
}

var legacyPeerWarned atomic.Bool

// warnLegacyPeer explains, once per process, what accepting the old handshake
// costs.
func warnLegacyPeer(log *logrus.Logger, other string) {
	if legacyPeerWarned.Swap(true) {
		return
	}
	log.Warnf("the %s is on the handshake from before v2.0.0: the tunnel token crosses the wire as it is, "+
		"and something on the path could pose as either end. Upgrade it — once it has connected the new way, "+
		"this tunnel stops accepting the old handshake.", other)
}

// peerSession is the session of the client currently seated, for sealing the
// destinations sent to it. nil means a client on the previous handshake.
type peerSession struct {
	p atomic.Pointer[network.PeerSession]
}

func (s *peerSession) Set(v *network.PeerSession) { s.p.Store(v) }
func (s *peerSession) Get() *network.PeerSession  { return s.p.Load() }
func (s *peerSession) Clear()                     { s.p.Store(nil) }
