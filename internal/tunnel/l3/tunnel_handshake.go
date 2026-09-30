package l3

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// The dialling side's handshake: when to negotiate, how, and how the peer's
// silence is noticed.

// handshakeLoop keeps the dialling side supplied with a live session: it
// negotiates the first one, and replaces it before it ages out.
//
// end ends the generation. It is called when the handshakes over a carrier
// whose flow the path can block have gone unanswered for flowStuckAfter; see
// there.
func (t *Tunnel) handshakeLoop(ctx context.Context, end func()) {
	ticker := time.NewTicker(rekeyCheck)
	defer ticker.Stop()

	var failingSince time.Time
	for {
		if t.peerSilent(time.Now()) {
			if t.peerAnswersProbe(ctx) {
				// Traffic that expects no answer — a one-way UDP stream, the
				// kernel's own IPv6 and multicast chatter on the interface —
				// looked exactly like a dead peer, and every 15 seconds of it
				// tore the session down and built it again. Users saw their
				// connections drop over and over on a tunnel that was fine.
				// The peer has now said, under the session's keys, that it is
				// there, so nothing is rebuilt.
				t.log.Debugf("l3: %s of unanswered traffic, but the peer answered a probe — it is one-way traffic, not a dead peer", peerSilentAfter)
			} else {
				t.log.Warnf("l3: nothing has come back from the peer for %s while this end "+
					"was sending, and it did not answer a probe — handshaking again "+
					"(it may have restarted)", peerSilentAfter)
				t.silentRekey.Store(true)
			}
		}
		if t.silentRekey.Load() || t.needsSession() {
			if err := t.negotiate(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				if failingSince.IsZero() {
					failingSince = time.Now()
				}
				if stuckFlowCarrier(t.cfg.Carrier) && time.Since(failingSince) >= flowStuckAfter {
					t.log.Warnf("l3: no handshake has been answered over %s for %s — the path may have "+
						"stopped passing this flow; reopening the carrier from new source ports",
						t.cfg.Carrier, flowStuckAfter)
					t.flowStuck.Store(true)
					end()
					return
				}
				t.log.Warnf("l3: handshake did not complete: %v — retrying", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(handshakeBackoff):
				}
				continue
			}
			failingSince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// livenessProbeSize is the inner size a liveness probe stands in for: small,
// because it asks only whether the peer is there, not what fits.
const livenessProbeSize = 64

// peerAnswersProbe asks the peer, under the current session, whether it is
// there. A few tries, each waiting the probe timeout, so one lost datagram is
// not taken for a dead peer.
func (t *Tunnel) peerAnswersProbe(ctx context.Context) bool {
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return false
		}
		if t.sendProbe(ctx, livenessProbeSize) {
			return true
		}
	}
	return false
}

// noteSent records that the dialling side has sent something that has not
// been answered yet. Once per batch, and a single load when a send is already
// outstanding, so the data path pays nothing it would notice.
func (t *Tunnel) noteSent() {
	if t.cfg.Mode != ModeDial || t.unanswered.Load() != 0 {
		return
	}
	t.unanswered.CompareAndSwap(0, time.Now().UnixNano())
}

// noteAnswered records that the peer is demonstrably alive.
func (t *Tunnel) noteAnswered() {
	if t.unanswered.Load() != 0 {
		t.unanswered.Store(0)
	}
}

// peerSilent reports whether the dialling side has been sending into silence
// for long enough to conclude the peer has lost the session.
func (t *Tunnel) peerSilent(now time.Time) bool {
	first := t.unanswered.Load()
	if first == 0 || now.Sub(time.Unix(0, first)) < peerSilentAfter {
		return false
	}
	// Cleared so the next window starts from the next send, not from this one:
	// a handshake that fails is retried by the loop, not re-triggered here.
	t.unanswered.Store(0)
	return true
}

// needsSession reports whether a handshake should be started.
func (t *Tunnel) needsSession() bool {
	t.mu.RLock()
	current := t.current
	t.mu.RUnlock()
	return current == nil || current.dueForRekey(time.Now())
}

// negotiate runs one handshake to completion, resending the same message until
// it is answered.
func (t *Tunnel) negotiate(ctx context.Context) error {
	// Re-resolved each round, so a peer whose address has changed — a dynamic
	// DNS name, a provider that renumbered — is found again without a
	// restart.
	if err := t.resolvePeer(); err != nil {
		return err
	}
	peer := t.peerAddr()
	if peer == nil {
		return errors.New("l3: no peer address")
	}

	t.mu.RLock()
	avoid := uint32(0)
	if t.current != nil {
		avoid = t.current.id
	}
	t.mu.RUnlock()

	// A timestamp unless this listener was recently found not to read them.
	// See freshness.go for why falling back is safe to do on its answer.
	t.mu.Lock()
	var fresh uint64
	if time.Now().After(t.legacyUntil) {
		fresh = t.freshClock.next(time.Now())
	}
	t.mu.Unlock()

	attempt, err := beginHandshakeFresh(t.cfg.Token, avoid, encapID(t.encap), fresh)
	if err != nil {
		return err
	}
	datagram := attempt.datagram()

	// Answers to a previous round are worthless now and would be mistaken for
	// this one's if left in the channel.
	t.drainReplies()

	for i := 0; i < handshakeAttempts; i++ {
		if _, err := t.carrier.WriteTo(datagram, peer); err != nil {
			return fmt.Errorf("l3: sending the handshake to %s: %w", peer, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case reply := <-t.replies:
			if reply.id != attempt.id {
				continue // an answer to something else
			}
			sess, err := attempt.complete(reply.body)
			if errors.Is(err, errPeerLegacy) {
				t.mu.Lock()
				t.legacyUntil = time.Now().Add(legacyRetry)
				t.mu.Unlock()
				t.log.Infof("l3: %s runs a build without handshake timestamps; using the older "+
					"handshake with it, and trying again in %s. Upgrading it closes handshake "+
					"replay for good", peer, legacyRetry)
				return t.negotiate(ctx)
			}
			if err != nil {
				return err
			}
			t.installDialed(sess)
			return nil
		case <-time.After(handshakeRetry):
		}
	}
	return fmt.Errorf("l3: %s did not answer in %d attempts", peer, handshakeAttempts)
}

func (t *Tunnel) drainReplies() {
	for {
		select {
		case <-t.replies:
		default:
			return
		}
	}
}

// resolvePeer refreshes the dialling side's notion of where the peer is.
func (t *Tunnel) resolvePeer() error {
	addr, err := net.ResolveUDPAddr("udp", t.cfg.Addr)
	if err != nil {
		// A resolution failure is not fatal while a previous answer is still
		// on hand: a brief DNS outage should not take the tunnel down.
		if t.peerAddr() != nil {
			t.log.Debugf("l3: could not re-resolve %s: %v", t.cfg.Addr, err)
			return nil
		}
		return fmt.Errorf("l3: resolving %q: %w", t.cfg.Addr, err)
	}
	t.setPeer(addr)
	return nil
}

// sameAddr reports whether two carrier addresses are the same peer.
//
// Compared field by field rather than through String(), because this runs on
// every packet the tunnel receives and String() builds a fresh string each
// time. Two allocations per packet is nothing at a handful of packets a second
// and is the garbage collector's whole workload at ten thousand — which is the
// shape of "CPU climbs with the connection count" when the connections
// themselves are cheap.
func sameAddr(a, b net.Addr) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	switch x := a.(type) {
	case *net.UDPAddr:
		y, ok := b.(*net.UDPAddr)
		return ok && x.Port == y.Port && x.IP.Equal(y.IP)
	case *net.IPAddr:
		y, ok := b.(*net.IPAddr)
		return ok && x.IP.Equal(y.IP)
	}
	// An address type this does not know: fall back to the string form, which
	// is correct for anything and slow for nothing that reaches here.
	return a.String() == b.String()
}

// foreignTagHooker is a carrier that can say when traffic for a different
// tunnel of its kind arrives — the xdi carrier, whose tag comes from the token.
type foreignTagHooker interface {
	SetForeignHook(func(from net.Addr))
}

// noteForeignTag is told about an xdi echo that carries the client's direction
// marker but another token's tag.
//
// On a listener that has no session, that is almost always its own peer with a
// token copied wrong: xdi drops such packets below the handshake, so nothing
// else on this side would ever say so, and the dialling side only reports that
// nobody answered. With a session up the same thing is only another tunnel's
// traffic on the same host, and is not worth a line.
func (t *Tunnel) noteForeignTag(from net.Addr) {
	t.mu.RLock()
	up := t.current != nil
	t.mu.RUnlock()
	if up {
		return
	}
	if n, say := t.foreignTags.allow(time.Now()); say {
		t.log.Warnf("l3: xdi echoes from %s carry a different tunnel's tag (%d so far) and "+
			"no session is up: if %s is this tunnel's other end, the token on the two "+
			"servers is not the same", from, n, from)
	}
}

// Up reports whether the tunnel has a session: a handshake has completed and
// its keys are in use.
func (t *Tunnel) Up() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.current != nil
}

// flowStuckAfter is how long the dialling side keeps handshaking over one flow
// that answers nothing before it opens the carrier again (see stuckFlowCarrier).
//
// Those carriers are a TCP flow as far as the path can tell, and a middlebox
// that stops passing one keeps dropping that flow — the same source port to the
// same destination — however often it is retried. Reported on v1.8.4: a direct
// pck tunnel died after about a day, restarts did not bring it back, and only a
// tunnel made again from scratch did. Reopening draws new source ports (see
// network.pckClientPortBase), which is what the new tunnel had. Long enough
// that a restarting kharej is simply waited for; a variable so a test need not.
var flowStuckAfter = 90 * time.Second

// stuckFlowCarrier reports whether a carrier's flow can be blocked by the path
// as a flow, and is worth reopening from new ports when it stops answering.
//
// Every carrier whose dialling end opens afresh from a new source — a new port
// for udp and quic, a new echo identifier for xdi — and not only the two that
// look like TCP. Reported on v1.8.4 again, for direct tunnels in general: one
// stopped answering, restarting it did nothing, and changing the port in the
// config brought it straight back. A UDP flow the path has stopped passing is
// the same five-tuple on every retry for as long as the socket is kept, which
// for a generation that never ends is for ever; changing the port was a new
// flow, which is all reopening gives. Spoof is left out: its source is the
// forged one, not a port this end can move.
func stuckFlowCarrier(carrier string) bool {
	switch carrier {
	case CarrierPck, CarrierSNI, CarrierUDP, "", CarrierXdi, CarrierQuic:
		return true
	}
	return false
}
