package l3

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/sirupsen/logrus"
)

// The engine.
//
// Two pumps and, on the dialling side, a handshake loop. The pumps are
// deliberately dumb: one moves packets from the TUN device to the carrier, the
// other moves them back, and neither knows anything about handshakes. All the
// state that makes a tunnel more than a pipe — which keys are current, which
// are being retired, where the peer is — lives in the small guarded block in
// the middle, and both pumps reach it through accessors.
//
// # Three sessions, not one
//
// A rekey cannot be instantaneous: packets sealed under the old keys are still
// in flight when the new ones come up, and dropping them would put a
// visible stall into every connection through the tunnel every two minutes.
// So a tunnel holds up to three sessions at once, each identified on the wire
// by its own id, and an arriving packet is matched to whichever one sealed it:
//
//   - current is what this end seals with.
//   - previous is the session current replaced. It still decrypts, briefly,
//     so packets already on the path are not lost.
//   - pending exists only on the listening side. See below.
//
// # Why the listener does not trust a handshake immediately
//
// A handshake message is authenticated — it cannot be forged without the token
// — but it can be recorded and sent again. If the listener installed each
// completed handshake as its current session, anyone who had captured one
// could replay it at will and repeatedly tear down the real peer's session,
// which is a denial of service costing one recorded datagram.
//
// So a completed handshake becomes pending, not current. It is promoted only
// when a data packet arrives that authenticates under it, which a replayer
// cannot produce: they would need the session's keys, and those come from an
// ephemeral exchange they cannot repeat. This is the same reasoning WireGuard
// applies to the same problem.
//
// # Learning where the peer is
//
// The listening side does not know its peer's address until it hears from it,
// and a peer's address can change mid-session. The address is therefore taken
// from arriving packets — but only from packets that have authenticated.
// Taking it from an unauthenticated datagram would let anyone redirect the
// tunnel by sending one forged packet from the address of their choice.

const (
	// handshakeRetry is how long the dialling side waits for a reply before
	// sending the same message again. It has to be a message the peer has not
	// answered rather than a fresh one: a new ephemeral key each attempt would
	// make a late reply to an earlier attempt unreadable.
	handshakeRetry = 800 * time.Millisecond

	// handshakeAttempts bounds one round before the loop backs off and starts
	// over, which is also what re-resolves a peer whose address has moved.
	handshakeAttempts = 6

	// handshakeBackoff is the pause between rounds, so an unreachable peer
	// costs a packet every few seconds rather than a busy loop.
	handshakeBackoff = 3 * time.Second

	// previousGrace is how long a replaced session keeps decrypting.
	previousGrace = 30 * time.Second

	// rekeyCheck is how often the dialling side looks at whether the current
	// session is due for replacement.
	rekeyCheck = 5 * time.Second
)

// Stats is what the tunnel reports about itself.
type Stats struct {
	PacketsIn, PacketsOut uint64
	BytesIn, BytesOut     uint64
	Dropped               uint64
	Handshakes            uint64
}

// packetDevice is the interface the engine has to the kernel: a source and
// sink of whole IP packets. The only implementation in the build is the TUN
// device, which exists on Linux alone — so this exists to let the engine, its
// session handling and its two pumps be exercised on any platform against a
// device that is not a device.
type packetDevice interface {
	// Read fills bufs with up to BatchSize packets and their lengths in sizes,
	// returning how many arrived.
	//
	// Batched, because a TUN read is a syscall and a busy tunnel does thousands
	// a second. With the kernel's segmentation offload on, one read can return
	// a whole 64 KB run of one TCP stream already split into MTU-sized packets
	// — forty-odd packets for the cost of one syscall. See tun_linux.go.
	Read(bufs [][]byte, sizes []int) (int, error)

	// Write injects packets into the kernel's routing.
	Write(bufs [][]byte) (int, error)

	// BatchSize is the most packets one Read or Write may move.
	BatchSize() int

	Close() error
	Name() string
	MTU() int

	// SetMTU changes the interface's MTU while it is up, which is what lets
	// the path be measured rather than guessed at. See mtuprobe.go.
	SetMTU(int) error
}

// deviceSpec is everything the TUN device is created with. A struct rather
// than a parameter list, because the list had reached seven and the next reader
// would have had to count commas to see which int was which.
type deviceSpec struct {
	Name       string
	LocalIP    string
	PeerIP     string
	MTU        int
	MSSClamp   int
	TxQueueLen int
	Qdisc      string
	Log        *logrus.Logger
}

// deviceSpecFor renders the spec from a validated config.
func (t *Tunnel) deviceSpecFor() deviceSpec {
	return deviceSpec{
		Name:       t.cfg.Iface,
		LocalIP:    t.cfg.LocalIP,
		PeerIP:     t.cfg.PeerIP,
		MTU:        t.cfg.MTU,
		MSSClamp:   t.cfg.MSSClamp,
		TxQueueLen: t.cfg.TxQueueLen,
		Qdisc:      t.cfg.Qdisc,
		Log:        t.log,
	}
}

// openDevice is what Run calls to get its device. Tests replace it.
func openDevice(spec deviceSpec) (packetDevice, error) {
	dev, err := openTUNTuned(spec.Name, spec.LocalIP, spec.PeerIP,
		spec.MTU, spec.MSSClamp, spec.TxQueueLen, spec.Qdisc, spec.Log)
	if err != nil {
		// A typed nil behind an interface is not a nil interface, and every
		// caller here tests the interface.
		return nil, err
	}
	return dev, nil
}

// Tunnel is one running layer-3 tunnel.
type Tunnel struct {
	cfg   Config
	encap Encap
	log   *logrus.Logger

	tun     packetDevice
	carrier DatagramCarrier

	// wrapCarrier is swapped by tests, to stand a constrained path in front of
	// the real one. Nil in production.
	wrapCarrier func(DatagramCarrier) DatagramCarrier

	// openDevice is swapped by tests; nil means the real TUN device.
	openDevice func(deviceSpec) (packetDevice, error)

	// localAddr is the carrier's own address, published once the carrier is
	// open. Separate from carrier itself, which the pumps read without a lock
	// because it is written before they are started.
	localAddrMu sync.RWMutex
	localAddr   net.Addr

	// mu guards everything below it. The critical sections are all short —
	// swapping a pointer, reading an address — and never span a syscall.
	mu       sync.RWMutex
	current  *session
	pending  *session
	previous *session
	prevFrom time.Time
	peer     net.Addr

	// probe is how patient the MTU search is; see mtuprobe.go.
	probe probeTiming

	// mtu is what the interface is currently set to, which the prober may move
	// away from what the config asked for. Guarded because the prober writes it
	// while the log and the management screens read it.
	mtuMu      sync.RWMutex
	mtuCurrent int

	// probeWaiters maps a probe's identifier to whoever is waiting for its
	// answer. The receive pump delivers; the prober waits.
	probeMu      sync.Mutex
	probeWaiters map[uint32]chan uint32

	// replies carries handshake answers from the receive pump to the
	// handshake loop. Buffered so the pump never blocks on it, and answers
	// that arrive with nobody waiting are simply dropped.
	replies chan handshakeReply

	// The listening side answers a retransmitted first message with the
	// identical reply rather than starting a second handshake, which would
	// derive keys the initiator has no way to arrive at.
	lastInitID uint32
	lastReply  []byte

	stats struct {
		packetsIn, packetsOut atomic.Uint64
		bytesIn, bytesOut     atomic.Uint64
		dropped               atomic.Uint64
		handshakes            atomic.Uint64
	}
}

type handshakeReply struct {
	id   uint32
	body []byte
}

// New validates a configuration and opens nothing. Open does the work, so a
// caller can reject a bad config without having touched the system.
func New(cfg Config, log *logrus.Logger) (*Tunnel, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	encap, err := NewEncap(cfg.Encap, cfg.GREKey)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = logrus.StandardLogger()
	}
	return &Tunnel{
		cfg:          cfg,
		encap:        encap,
		log:          log,
		replies:      make(chan handshakeReply, 4),
		mtuCurrent:   cfg.MTU,
		probe:        defaultProbeTiming(),
		probeWaiters: make(map[uint32]chan uint32),
	}, nil
}

// Run opens the device and the carrier and serves the tunnel until ctx ends.
// It always cleans up what it opened, including on the error paths.
func (t *Tunnel) Run(ctx context.Context) error {
	carrier, peer, err := openCarrier(t.cfg)
	if err != nil {
		return err
	}
	if t.wrapCarrier != nil {
		carrier = t.wrapCarrier(carrier)
	}
	t.carrier = carrier
	t.setPeer(peer)
	t.localAddrMu.Lock()
	t.localAddr = carrier.LocalAddr()
	t.localAddrMu.Unlock()
	defer carrier.Close()

	open := t.openDevice
	if open == nil {
		open = openDevice
	}
	tun, err := open(t.deviceSpecFor())
	if err != nil {
		return err
	}
	t.tun = tun
	t.setCurrentMTU(tun.MTU())
	defer tun.Close()

	suggested := MTUFor(1500, carrier.Overhead(), t.encap.Overhead())
	t.log.Infof("l3: %s up on %s, %s over %s, mtu %d (a 1500-byte path fits %d)",
		t.cfg.Mode, tun.Name(), t.encap.Name(), carrier.CarrierName(), t.cfg.MTU, suggested)
	if t.cfg.MTU > suggested {
		t.log.Warnf("l3: mtu %d exceeds what a 1500-byte path carries (%d); large packets will fragment or be dropped",
			t.cfg.MTU, suggested)
	}

	// Unblock both pumps when the context ends. A read on either device
	// blocks indefinitely, and closing is the portable way to interrupt it.
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
		case <-stopped:
		}
		carrier.Close()
		tun.Close()
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); t.pumpFromCarrier(ctx) }()
	go func() { defer wg.Done(); t.pumpFromTUN(ctx) }()

	if t.cfg.Mode == ModeDial {
		wg.Add(1)
		go func() { defer wg.Done(); t.handshakeLoop(ctx) }()
	}

	// Both ends probe: each measures what it can send, and sets its own
	// interface. See mtuprobe.go for why that is better than agreeing on one
	// shared figure.
	wg.Add(1)
	go func() { defer wg.Done(); t.probeLoop(ctx) }()

	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return errors.New("l3: the tunnel stopped unexpectedly")
}

// LocalAddr is the address the carrier is bound to, or nil before Run has
// opened it. On the listening side with port 0 configured, this is how the
// actual port is discovered.
func (t *Tunnel) LocalAddr() net.Addr {
	t.localAddrMu.RLock()
	defer t.localAddrMu.RUnlock()
	return t.localAddr
}

// Stats returns a snapshot for diagnostics.
func (t *Tunnel) Stats() Stats {
	return Stats{
		PacketsIn:  t.stats.packetsIn.Load(),
		PacketsOut: t.stats.packetsOut.Load(),
		BytesIn:    t.stats.bytesIn.Load(),
		BytesOut:   t.stats.bytesOut.Load(),
		Dropped:    t.stats.dropped.Load(),
		Handshakes: t.stats.handshakes.Load(),
	}
}

// ---------------------------------------------------------------- state

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
// manage.datagramServerPeer for the reading half, which treats a snapshot
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

// ---------------------------------------------------------------- pumps

// pumpFromTUN reads packets the kernel routed into the interface, seals them
// and sends them to the peer.
func (t *Tunnel) pumpFromTUN(ctx context.Context) {
	batch := t.tun.BatchSize()
	if batch < 1 {
		batch = 1
	}
	bufs := make([][]byte, batch)
	for i := range bufs {
		bufs[i] = make([]byte, t.cfg.MTU)
	}
	sizes := make([]int, batch)

	frame := make([]byte, 0, t.cfg.MTU+t.encap.Overhead())
	out := make([]byte, 0, t.cfg.MTU+t.encap.Overhead()+dataOverhead)

	for {
		count, err := t.tun.Read(bufs, sizes)
		if err != nil {
			if ctx.Err() == nil {
				t.log.Errorf("l3: reading from %s: %v", t.cfg.Iface, err)
			}
			return
		}

		// Resolved once per batch rather than once per packet. Both take the
		// state lock, and at a few thousand packets a second it was being taken
		// twice as often as anything in it changed.
		sess := t.sendSession()
		peer := t.peerAddr()

		for i := 0; i < count; i++ {
			n := sizes[i]
			if n == 0 {
				continue
			}
			if sess == nil || peer == nil {
				// Nothing to send under yet. Dropping is correct: the layer
				// above owns retransmission, and queueing here would only
				// deliver a burst of stale packets once the tunnel came up.
				t.stats.dropped.Add(1)
				continue
			}

			wrapped, err := t.encap.Wrap(frame[:0], bufs[i][:n])
			if err != nil {
				t.stats.dropped.Add(1)
				t.log.Debugf("l3: not forwarding a packet off %s: %v", t.cfg.Iface, err)
				continue
			}
			sealed, err := sess.seal(out[:0], wrapped)
			if err != nil {
				t.stats.dropped.Add(1)
				t.log.Warnf("l3: sealing a packet: %v", err)
				continue
			}
			if _, err := t.carrier.WriteTo(sealed, peer); err != nil {
				if ctx.Err() != nil {
					return
				}
				t.stats.dropped.Add(1)
				t.log.Debugf("l3: sending to %s: %v", peer, err)
				continue
			}
			t.stats.packetsOut.Add(1)
			t.stats.bytesOut.Add(uint64(n))
		}
	}
}

// pumpFromCarrier reads datagrams off the carrier and routes them by kind.
func (t *Tunnel) pumpFromCarrier(ctx context.Context) {
	buf := make([]byte, maxMTU+256)
	plain := make([]byte, 0, maxMTU+256)
	ticker := time.NewTicker(previousGrace / 2)
	defer ticker.Stop()

	// Retiring old sessions is timer work with nothing else to do it, and the
	// receive pump is the one goroutine guaranteed to exist on both sides.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				t.retireSessions(now)
			}
		}
	}()

	for {
		n, from, err := t.carrier.ReadFrom(buf)
		if err != nil {
			if ctx.Err() == nil {
				t.log.Errorf("l3: reading from the carrier: %v", err)
			}
			return
		}

		h, body, err := parseHeader(buf[:n])
		if err != nil {
			// A stray datagram on an open port: a scanner, a stale peer, or
			// noise. Not worth a log line above debug.
			t.stats.dropped.Add(1)
			continue
		}

		switch h.kind {
		case typeInit:
			t.handleInit(h, body, from)
		case typeResp:
			t.handleResp(h, body)
		case typeData:
			plain = t.handleData(plain, h, body, from)
		case typeProbe, typeProbeAck:
			plain = t.handleProbeMessage(plain, h, body, from)
		}
	}
}

// handleInit is the listening side's half of the handshake.
func (t *Tunnel) handleInit(h header, body []byte, from net.Addr) {
	if t.cfg.Mode != ModeListen {
		return // the dialling side does not answer handshakes
	}

	// A retransmission of a message already answered gets the same answer
	// back. Deriving a second set of keys for the same session would leave the
	// initiator holding keys this end has thrown away.
	t.mu.Lock()
	if h.session == t.lastInitID && t.lastReply != nil {
		reply := t.lastReply
		t.mu.Unlock()
		_, _ = t.carrier.WriteTo(reply, from)
		return
	}
	t.mu.Unlock()

	sess, reply, err := respond(t.cfg.Token, h.session, body, encapID(t.encap))
	if err != nil {
		// A mismatched encapsulation is a misconfiguration, not an intruder:
		// the peer proved it holds the token, so it is told, loudly, and its
		// reply is still sent so it can say the same thing in its own log.
		// Everything else is met with silence — a peer without the token learns
		// nothing, not even that something is listening.
		if reply != nil {
			t.log.Errorf("l3: refusing the tunnel from %s: %v", from, err)
			_, _ = t.carrier.WriteTo(reply, from)
		} else {
			t.log.Debugf("l3: refusing a handshake from %s: %v", from, err)
		}
		t.stats.dropped.Add(1)
		return
	}

	t.mu.Lock()
	t.pending = sess
	t.lastInitID = h.session
	t.lastReply = reply
	// The address is provisional until a data packet confirms it, which is
	// also what promotes the session. Recording it here is what makes the
	// reply deliverable at all.
	if t.current == nil {
		t.peer = from
	}
	t.mu.Unlock()

	// Say that a peer is here, now, rather than waiting for it to send
	// something.
	//
	// Promotion deliberately waits for an authenticated data packet, because
	// installing a replayed handshake as the live session would be a denial of
	// service costing one recorded datagram. That reasoning is about which keys
	// the tunnel seals with. It is not about what the management screens say,
	// and applying it to them had a cost nobody intended: the listening side
	// published no peer until traffic happened to cross, so a tunnel that was
	// up and simply idle read as offline on one machine and online on the
	// other. A completed handshake proves the peer holds the token, which is
	// exactly what "a peer is connected" means.
	t.publishPeer()

	if _, err := t.carrier.WriteTo(reply, from); err != nil {
		t.log.Debugf("l3: answering a handshake to %s: %v", from, err)
	}
}

// handleResp hands a handshake answer to the loop waiting for it.
func (t *Tunnel) handleResp(h header, body []byte) {
	if t.cfg.Mode != ModeDial {
		return
	}
	// The body aliases the read buffer, which the next read overwrites.
	reply := handshakeReply{id: h.session, body: append([]byte(nil), body...)}
	select {
	case t.replies <- reply:
	default:
		// Nobody waiting, or the buffer is full of stale answers. Either way
		// this one is not wanted.
	}
}

// handleData decrypts one packet and writes it into the interface. It returns
// the plaintext buffer so its capacity is carried into the next call.
func (t *Tunnel) handleData(plain []byte, h header, body []byte, from net.Addr) []byte {
	sess, isPending := t.sessionFor(h.session)
	if sess == nil {
		t.stats.dropped.Add(1)
		return plain
	}

	opened, err := sess.open(plain, h, body)
	if err != nil {
		t.stats.dropped.Add(1)
		t.log.Debugf("l3: discarding a datagram from %s: %v", from, err)
		return plain
	}
	// Keep whichever buffer is larger, so the capacity settles rather than
	// being reallocated per packet.
	if cap(opened) > cap(plain) {
		plain = opened[:0]
	}

	// The packet authenticated, so everything it implies can now be trusted:
	// that these keys are live, and that this is where the peer is.
	if isPending {
		t.promote(sess)
	}
	t.notePeer(from)

	inner, err := t.encap.Unwrap(opened)
	if err != nil {
		t.stats.dropped.Add(1)
		t.log.Debugf("l3: discarding a malformed inner packet from %s: %v", from, err)
		return plain
	}
	if _, err := t.tun.Write([][]byte{inner}); err != nil {
		t.stats.dropped.Add(1)
		t.log.Debugf("l3: writing to %s: %v", t.cfg.Iface, err)
		return plain
	}
	t.stats.packetsIn.Add(1)
	t.stats.bytesIn.Add(uint64(len(inner)))
	return plain
}

// notePeer follows a peer that has moved, which is safe only because the
// caller has already authenticated the packet the address came from.
func (t *Tunnel) notePeer(from net.Addr) {
	if from == nil {
		return
	}
	t.mu.RLock()
	same := sameAddr(t.peer, from)
	t.mu.RUnlock()
	if same {
		return
	}
	t.mu.Lock()
	previous := t.peer
	t.peer = from
	t.mu.Unlock()
	if previous != nil {
		t.log.Infof("l3: peer moved from %s to %s", previous, from)
	}
}

// ---------------------------------------------------------------- handshake

// handshakeLoop keeps the dialling side supplied with a live session: it
// negotiates the first one, and replaces it before it ages out.
func (t *Tunnel) handshakeLoop(ctx context.Context) {
	ticker := time.NewTicker(rekeyCheck)
	defer ticker.Stop()

	for {
		if t.needsSession() {
			if err := t.negotiate(ctx); err != nil {
				if ctx.Err() != nil {
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
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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

	attempt, err := beginHandshake(t.cfg.Token, avoid, encapID(t.encap))
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
