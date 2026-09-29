package l3

import (
	"net"
	"time"

	"github.com/backpack/backpack/internal/metrics"
)

// The tunnel's session state: which session seals, which opens, and how a
// session becomes current and is retired. See the package doc for the model.

func (t *Tunnel) setPeer(addr net.Addr) {
	if addr == nil {
		return
	}
	t.mu.Lock()
	t.peer = addr
	t.mu.Unlock()
}

func (t *Tunnel) peerAddr() net.Addr {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.peer
}

// sendSession is the session outgoing packets are sealed under: the confirmed
// one, or the unconfirmed one while the tunnel is still coming up. Once a
// session has been confirmed, an unconfirmed one is never sealed with — that
// is what stops a replayed handshake from diverting the outgoing direction.
func (t *Tunnel) sendSession() *session {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.current != nil {
		return t.current
	}
	return t.pending
}

// sessionFor finds the keys a received packet was sealed under.
func (t *Tunnel) sessionFor(id uint32) (sess *session, isPending bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	switch {
	case t.current != nil && t.current.id == id:
		return t.current, false
	case t.pending != nil && t.pending.id == id:
		return t.pending, true
	case t.previous != nil && t.previous.id == id:
		return t.previous, false
	}
	return nil, false
}

// installDialed makes a freshly negotiated session current. The dialling side
// initiated it, so there is nothing to confirm.
func (t *Tunnel) installDialed(sess *session) {
	t.mu.Lock()
	replaced := t.current != nil
	t.previous, t.prevFrom = t.current, time.Now()
	t.current = sess
	t.pending = nil
	t.mu.Unlock()
	t.stats.handshakes.Add(1)
	t.publishPeer()
	t.noteAnswered()

	// A handshake the silence started is a recovery, not a routine rekey, and
	// must not say the tunnel held — it did not.
	if t.silentRekey.Swap(false) && replaced {
		t.log.Infof("l3: session %08x re-established after the peer went silent", sess.id)
		return
	}

	// A rekey is not a reconnection, and saying "established" for both made a
	// healthy tunnel look like one that drops every two minutes. Somebody read
	// that log, quite reasonably, as the tunnel flapping — and went looking for
	// a fault that was not there. The line has to distinguish the two.
	if replaced {
		t.log.Infof("l3: rekeyed to session %08x (routine, every %s — the tunnel did not drop)",
			sess.id, rekeyAfterTime)
		return
	}
	t.log.Infof("l3: session %08x established", sess.id)
}

// promote makes a pending session current, called when a packet has proved the
// peer holds its keys.
func (t *Tunnel) promote(sess *session) {
	t.mu.Lock()
	if t.pending != sess {
		t.mu.Unlock()
		return // already promoted by a packet that raced this one
	}
	replaced := t.current != nil
	t.previous, t.prevFrom = t.current, time.Now()
	t.current = sess
	t.pending = nil
	if sess.adoptFresh != 0 {
		t.fresh.adopt(sess.adoptFresh, sess.adoptOver)
	}
	t.mu.Unlock()
	t.stats.handshakes.Add(1)
	t.publishPeer()

	// Same distinction as installDialed, on the side that is told to rekey
	// rather than deciding to.
	if replaced {
		t.log.Infof("l3: rekeyed to session %08x (routine — the tunnel did not drop)", sess.id)
		return
	}
	t.log.Infof("l3: session %08x confirmed", sess.id)
}

// currentMTU is what the interface is set to now, which the prober may have
// moved away from the configured figure.
func (t *Tunnel) currentMTU() int {
	t.mtuMu.RLock()
	defer t.mtuMu.RUnlock()
	return t.mtuCurrent
}

func (t *Tunnel) setCurrentMTU(mtu int) {
	t.mtuMu.Lock()
	t.mtuCurrent = mtu
	t.mtuMu.Unlock()
}

// publishPeer records where the far end is, for the management screens.
//
// Nothing in the kernel can answer "is this tunnel up?" for a layer-3 tunnel:
// udp holds an unconnected socket and the raw carriers do not go through the
// stack at all, so the socket table — which is what the health check and the
// watchdog read for every other kind — has nothing to show. Left at that, a
// perfectly healthy tunnel appears on the panel as a grey card with no state.
//
// The engine does know, so it writes it down. This is the same channel the
// datagram transports already use for the same reason; see
// manage.datagramPeer for the reading half, which treats a snapshot
// older than a couple of intervals as saying nothing rather than as a peer.
func (t *Tunnel) publishPeer() {
	if peer := t.peerAddr(); peer != nil {
		metrics.ReportPeer(peer.String())
	}
}

// retireSessions drops a replaced session once its grace period is over, and
// any session — current included — that has outlived rejectAfterTime.
//
// # Why current is expired too
//
// rejectAfterTime is documented as the point a session stops being usable at
// all, and for a while nothing enforced that against the current session: only
// previous and pending were ever dropped. A tunnel whose peer had gone away
// therefore held its last session forever. The data path barely noticed —
// packets sealed under keys nobody holds are dropped at the far end, if there
// still is one — but the management screens did. The peer written to the
// metrics snapshot was never cleared, so a tunnel whose far end had been down
// for hours went on showing "peer connected" on the panel, which is the exact
// failure the snapshot was added to prevent.
//
// The gap between rekeyAfterTime and rejectAfterTime — two minutes against
// five — is the window a rekey has to complete in, and it is deliberately
// generous. Reaching the far end of it means three minutes of failed
// handshakes, which is a tunnel that is down whatever the panel says.
func (t *Tunnel) retireSessions(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.previous != nil && (now.Sub(t.prevFrom) > previousGrace || t.previous.expired(now)) {
		t.previous = nil
	}
	if t.pending != nil && t.pending.expired(now) {
		t.pending = nil
	}
	if t.current != nil && t.current.expired(now) {
		t.current = nil
	}
	// Nothing left that could carry a packet: say so, rather than leaving a
	// stale address that reads as a live tunnel.
	if t.current == nil && t.pending == nil {
		metrics.ClearPeer()
	}
}
