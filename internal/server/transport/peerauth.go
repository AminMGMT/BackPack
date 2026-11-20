package transport

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/gorilla/websocket"
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

// The plain ws transports carry the handshake in the upgrade: the client's
// hello as its bearer on /channel, the server's reply as a response header,
// and the client's confirm as the first message on the new websocket. Pool
// connections (/tunnel) present the session's pool tag as their bearer. Over
// wss the TLS-bound proofs already do this job (see network.WSSServerAnswer)
// and nothing here applies.
const (
	wsBearerHello = network.PeerAuthWire + "."
	wsBearerPool  = network.PeerAuthWire + "p."
)

// wsPeerAuth is a ws transport's side of it.
type wsPeerAuth struct {
	guard      *network.PeerGuard
	token      string
	simpleAuth bool
	label      string
	peer       *peerSession
	log        *logrus.Logger
}

// wsAdmission is what an upgrade request proved.
type wsAdmission struct {
	// session is set on a /channel claim made with the new handshake, before
	// the confirm; nil for every other request.
	session *network.PeerSession
	// header is what the upgrade answers with.
	header map[string][]string
}

// admit judges an upgrade request. ok false means the request gets the decoy.
func (a wsPeerAuth) admit(r *http.Request) (adm wsAdmission, ok bool) {
	// wss proves both ends through its TLS session already, and with
	// simple_auth a trusted proxy in front stands in for this server.
	if r.TLS != nil || a.simpleAuth || !websocket.IsWebSocketUpgrade(r) {
		if !isTunnelRequest(r, a.token, a.simpleAuth) {
			return adm, false
		}
		return wsAdmission{header: serverProof(r, a.token, a.simpleAuth)}, true
	}
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch {
	case r.URL.Path == "/channel" && strings.HasPrefix(bearer, wsBearerHello):
		h, good := network.DecodePeerHello(strings.TrimPrefix(bearer, wsBearerHello))
		if !good || !a.guard.Key().VerifyHello(h, a.label) {
			return adm, false
		}
		reply, sess, err := a.guard.Key().Reply(h, a.label)
		if err != nil {
			return adm, false
		}
		return wsAdmission{session: sess, header: map[string][]string{network.PeerAuthHeader: {reply.Encode()}}}, true
	case strings.HasPrefix(r.URL.Path, "/tunnel") && strings.HasPrefix(bearer, wsBearerPool):
		sess := a.peer.Get()
		if sess == nil {
			return adm, false
		}
		tag := strings.TrimPrefix(bearer, wsBearerPool)
		return adm, subtle.ConstantTimeCompare([]byte(tag), []byte(sess.PoolTag())) == 1
	}
	// The token itself: a client on the handshake before v2.0.0.
	if !isTunnelRequest(r, a.token, a.simpleAuth) {
		return adm, false
	}
	if r.URL.Path == "/channel" {
		if a.guard.Strict() {
			a.log.Warnf("a client at %s offered the handshake from before v2.0.0 and was refused: %s",
				r.RemoteAddr, strictWhy(a.guard))
			return adm, false
		}
		warnLegacyPeer(a.log, "client")
		return adm, true
	}
	// A pool connection on the token is a legacy client's, and only while one
	// is seated.
	return adm, a.peer.Get() == nil && !a.guard.Strict()
}

// confirm reads the client's confirm off a new /channel websocket made with
// the new handshake. A nil session (the old handshake) has nothing to read.
func (a wsPeerAuth) confirm(conn *websocket.Conn, sess *network.PeerSession) bool {
	if sess == nil {
		return true
	}
	_ = conn.SetReadDeadline(time.Now().Add(announceTimeout))
	_, msg, err := conn.ReadMessage()
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil || !sess.CheckConfirm(string(msg)) {
		a.log.Warnf("a control channel claim from %s did not finish the handshake", conn.RemoteAddr())
		conn.Close()
		return false
	}
	a.guard.Proved()
	return true
}
