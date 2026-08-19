package l3

import (
	"context"
	"encoding/binary"
	"errors"
	"math/rand"
	"net"
	"time"
)

// Measuring the path instead of guessing at it.
//
// The MTU is the one setting on a layer-3 tunnel that cannot be worked out from
// the configuration, and the one that fails worst when it is wrong. Set it too
// high and the tunnel comes up, passes every liveness check, carries ping and
// SSH — and stalls every download and every TLS handshake, because the packets
// that matter are the large ones and they are dropped somewhere out on the path
// with no message coming back.
//
// Until this existed the engine printed a guess: "a 1500-byte path fits 1415".
// That guess assumes the whole route carries 1500 bytes, which a real route
// frequently does not — a PPPoE hop, an encapsulating provider, a tunnel
// upstream. On one pair of servers the real figure was 1371 against a
// configured 1400, and the 29-byte difference cost an afternoon: everything
// looked healthy and nothing worked.
//
// So the tunnel measures it. Once a session is up, it binary-searches for the
// largest packet the path will actually carry, and sets the interface to match.
//
// # Why each end measures only its own sending direction
//
// A path can carry more one way than the other, and what an interface MTU
// governs is what this host may send. Forcing both ends onto a single shared
// figure would mean the better direction is throttled to the worse one for no
// reason. So each end probes outward and sets its own interface, which is both
// simpler — nothing has to be negotiated — and more accurate.
//
// # Why the probe has to be padded
//
// A data message carrying an inner packet of P bytes costs, on the wire:
//
//	carrier + headerLen + (encapOverhead + P) + tagLen
//
// The probe is padded so its own wire size is exactly that. A smaller probe
// would arrive when a real packet of the size being tested would not, and the
// tunnel would conclude the path is wider than it is — which is the failure
// this exists to prevent, arrived at by a different route.
//
// # Safety
//
// Probes are sealed under the session, so only a peer holding the token can
// answer one, and nobody else can push this tunnel's MTU around. A peer built
// before probes existed does not recognise the message kind and drops it; the
// search then finds nothing and the configured MTU is left exactly as it was.

const (
	// probeTimeout is how long one size waits for its answer. Generous,
	// because a probe is a full-sized packet on a slow path.
	probeTimeout = 2 * time.Second

	// probeAttempts is how many times one size is tried before it is called a
	// failure. A single lost datagram must not be read as "too big", or the
	// search settles far below the truth on a lossy link.
	probeAttempts = 3

	// probeSettle is how long to wait after a session comes up before probing.
	// The first moments of a tunnel are its busiest.
	probeSettle = 2 * time.Second

	// probeEvery is how often the measurement is repeated. Paths change —
	// a provider reroutes, an upstream tunnel appears — and a figure measured
	// once at boot slowly stops being true.
	probeEvery = 30 * time.Minute

	// probeWarnBelow is the point at which a measured path is worth complaining
	// about rather than merely reporting. IPv6 requires 1280, so a tunnel
	// measured under it carries IPv4 and not much else.
	probeWarnBelow = 1280
)

var errProbeUnanswered = errors.New("l3: the peer did not answer any probe")

// probeBody is the plaintext of a probe: an identifier, the size being tested,
// and padding out to the length that makes the datagram the right size.
const probeHeaderLen = 6 // 4-byte id + 2-byte size

// buildProbe renders the plaintext for a probe of inner-packet size p. The
// plaintext stands in for what a data message would carry — the encapsulation
// header plus the inner packet — so the sealed datagram comes out the same size.
func buildProbe(id uint32, p, encapOverhead int) []byte {
	body := make([]byte, encapOverhead+p)
	binary.BigEndian.PutUint32(body[0:4], id)
	binary.BigEndian.PutUint16(body[4:6], uint16(p))
	return body
}

// readProbe reads back what buildProbe wrote.
func readProbe(body []byte) (id uint32, p int, ok bool) {
	if len(body) < probeHeaderLen {
		return 0, 0, false
	}
	return binary.BigEndian.Uint32(body[0:4]), int(binary.BigEndian.Uint16(body[4:6])), true
}

// probeAck is the answer: the identifier and the size that arrived.
func buildProbeAck(id uint32, p int) []byte {
	ack := make([]byte, probeHeaderLen)
	binary.BigEndian.PutUint32(ack[0:4], id)
	binary.BigEndian.PutUint16(ack[4:6], uint16(p))
	return ack
}

// probeLoop measures the path and keeps the interface in step with it.
func (t *Tunnel) probeLoop(ctx context.Context) {
	if !t.cfg.AutoMTU {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(t.probe.settle):
	}

	for {
		t.measureAndApply(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(t.probe.every):
		}
	}
}

// probeTiming is how patient the search is. Constants in production; a test
// shrinks them so a full binary search does not take a minute of wall clock.
type probeTiming struct {
	timeout  time.Duration
	attempts int
	settle   time.Duration
	every    time.Duration
}

func defaultProbeTiming() probeTiming {
	return probeTiming{
		timeout:  probeTimeout,
		attempts: probeAttempts,
		settle:   probeSettle,
		every:    probeEvery,
	}
}

// measureAndApply runs one search and puts the result on the interface.
func (t *Tunnel) measureAndApply(ctx context.Context) {
	found, err := t.searchMTU(ctx)
	if err != nil {
		// Not an error worth alarming about: an older peer answers no probes,
		// and a tunnel that has just lost its session will be probed again on
		// the next round. What matters is that the configured MTU is untouched.
		t.log.Debugf("l3: could not measure the path (%v) — keeping mtu %d", err, t.cfg.MTU)
		return
	}
	if found == t.currentMTU() {
		return
	}
	if err := t.tun.SetMTU(found); err != nil {
		t.log.Warnf("l3: measured the path at %d but could not set the interface: %v", found, err)
		return
	}
	previous := t.currentMTU()
	t.setCurrentMTU(found)
	switch {
	case found < previous:
		t.log.Warnf("l3: the path only carries %d bytes, not %d — interface lowered to %d. "+
			"Large transfers would otherwise stall while ping kept working.", found, previous, found)
	default:
		t.log.Infof("l3: the path carries %d bytes — interface raised from %d", found, previous)
	}
	if found < probeWarnBelow {
		t.log.Warnf("l3: %d is below the %d IPv6 requires, so IPv6 will not cross this tunnel",
			found, probeWarnBelow)
	}
}

// searchMTU binary-searches for the largest inner packet the path carries.
func (t *Tunnel) searchMTU(ctx context.Context) (int, error) {
	ceiling := MTUFor(1500, t.carrier.Overhead(), t.encap.Overhead())
	if ceiling > maxMTU {
		ceiling = maxMTU
	}
	// minMTU, not 1280. A path too narrow for IPv6 is a bad path, but refusing
	// to measure it leaves the tunnel at a figure that does not work at all —
	// which is worse than a small one that does.
	floor := minMTU

	// The ceiling is tried first. On a clean path it succeeds immediately and
	// the whole search costs one packet.
	if t.probeOnce(ctx, ceiling) {
		return ceiling, nil
	}
	if !t.probeOnce(ctx, floor) {
		// Nothing crosses at all, or the peer answers no probes. Either way
		// there is no measurement to act on.
		return 0, errProbeUnanswered
	}

	best := floor
	lo, hi := floor+1, ceiling-1
	for lo <= hi {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		mid := (lo + hi) / 2
		if t.probeOnce(ctx, mid) {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best, nil
}

// probeOnce reports whether a packet carrying p bytes of inner packet reaches
// the peer, retrying so that one lost datagram is not mistaken for a size the
// path cannot carry.
func (t *Tunnel) probeOnce(ctx context.Context, p int) bool {
	for attempt := 0; attempt < t.probe.attempts; attempt++ {
		if ctx.Err() != nil {
			return false
		}
		if t.sendProbe(ctx, p) {
			return true
		}
	}
	return false
}

// sendProbe sends one probe and waits for its answer.
func (t *Tunnel) sendProbe(ctx context.Context, p int) bool {
	sess := t.sendSession()
	peer := t.peerAddr()
	if sess == nil || peer == nil {
		return false
	}

	id := rand.Uint32()
	sealed, err := sess.sealKind(nil, buildProbe(id, p, t.encap.Overhead()), typeProbe)
	if err != nil {
		return false
	}

	// Registered before the send, or a fast answer could arrive with nobody
	// listening for it.
	answers := make(chan uint32, 1)
	t.probeMu.Lock()
	t.probeWaiters[id] = answers
	t.probeMu.Unlock()
	defer func() {
		t.probeMu.Lock()
		delete(t.probeWaiters, id)
		t.probeMu.Unlock()
	}()

	if _, err := t.carrier.WriteTo(sealed, peer); err != nil {
		return false
	}

	select {
	case <-ctx.Done():
		return false
	case <-answers:
		return true
	case <-time.After(t.probe.timeout):
		return false
	}
}

// handleProbeMessage decrypts a probe or its answer and routes it.
//
// It goes through the same authentication as data — the same session, the same
// replay window — so a probe cannot be forged or replayed, and nobody without
// the token can move this tunnel's MTU. The plaintext buffer is threaded
// through exactly as the data path threads it, so its capacity is reused.
func (t *Tunnel) handleProbeMessage(plain []byte, h header, body []byte, from net.Addr) []byte {
	sess, isPending := t.sessionFor(h.session)
	if sess == nil {
		t.stats.dropped.Add(1)
		return plain
	}
	opened, err := sess.open(plain, h, body)
	if err != nil {
		t.stats.dropped.Add(1)
		return plain
	}
	if cap(opened) > cap(plain) {
		plain = opened[:0]
	}

	// A probe authenticates exactly as a data packet does, so it carries the
	// same two implications: these keys are live, and this is where the peer
	// is. Acting on them here is what lets a tunnel that has no user traffic
	// yet still confirm its session.
	if isPending {
		t.promote(sess)
	}
	t.notePeer(from)

	switch h.kind {
	case typeProbe:
		t.answerProbe(sess, opened)
	case typeProbeAck:
		t.handleProbeAck(opened)
	}
	return plain
}

// answerProbe replies to a peer's measurement.
func (t *Tunnel) answerProbe(sess *session, body []byte) {
	id, p, ok := readProbe(body)
	if !ok {
		return
	}
	ack, err := sess.sealKind(nil, buildProbeAck(id, p), typeProbeAck)
	if err != nil {
		return
	}
	if peer := t.peerAddr(); peer != nil {
		_, _ = t.carrier.WriteTo(ack, peer)
	}
}

// handleProbeAck wakes whichever probe was waiting for this answer.
func (t *Tunnel) handleProbeAck(body []byte) {
	id, _, ok := readProbe(body)
	if !ok {
		return
	}
	t.probeMu.Lock()
	ch := t.probeWaiters[id]
	t.probeMu.Unlock()
	if ch == nil {
		return // timed out already, or never ours
	}
	select {
	case ch <- id:
	default:
	}
}
