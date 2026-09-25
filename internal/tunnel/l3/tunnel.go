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
)

// rekeyCheck is how often the dialling side looks at whether the current
// session is due for replacement, or has stopped being answered. A variable so
// the test that restarts a listener under a live dialler need not wait on it.
var rekeyCheck = 5 * time.Second

// peerSilentAfter is how long the dialling side keeps sending into a session
// that nothing comes back on before it handshakes again.
//
// There was no such limit. A listener that restarted — an update, a config
// edit, a reboot — came back with no memory of the session, dropped every
// packet sealed under it, and the dialler went on sealing under it until the
// routine rekey two minutes later, then logged that rekey as "the tunnel did
// not drop". Measured: 122 seconds of black hole after a one-second restart.
//
// Fifteen seconds is WireGuard's number for the same question (keepalive plus
// rekey timeout), and for the same reason: long enough that a pause in a
// reply-less flow is not mistaken for a dead peer, short enough to be a blip.
// The cost of being wrong is one handshake — the old session keeps decrypting
// through previousGrace — so erring early is cheap.
var peerSilentAfter = 15 * time.Second

// Stats is what the tunnel reports about itself.
type Stats struct {
	PacketsIn, PacketsOut uint64
	BytesIn, BytesOut     uint64
	Dropped               uint64
	Handshakes            uint64
}

// tunReadBuf is how long every buffer handed to packetDevice.Read must be.
//
// It is 64 KB and not the MTU, which is the whole of a crash reported from the
// field: a layer-3 tunnel died with a memory fault inside the device read, came
// back, and died again.
//
// With segmentation offload on, a read does not return packets the kernel built
// to fit this interface. It returns one large run — up to 64 KB — and the
// library splits it into the segments the *sender* chose, whose size came from
// the sender's path and has nothing to do with the MTU here. The split writes
// each segment into the buffer at its index and does not check that it fits, so
// one segment larger than the buffer is not a short read or an error: it is a
// write past the end of a slice, which takes the process down with it.
//
// 64 KB is the bound that makes that impossible rather than unlikely — it is
// the most a single read can return, so no segment of it can be longer. It is
// also what wireguard-go's own device allocates, for this reason.
//
// The cost is smaller than it looks. A batch of these is a few megabytes of
// address space, and only the first page or so of each is ever written, so what
// the process actually occupies is close to what it was.
const tunReadBuf = 65535

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
	// a whole 64 KB run of one flow, split for us into its segments — dozens of
	// packets for the cost of one syscall. See tun_linux.go.
	//
	// Every buffer must be tunReadBuf long. Not the MTU: the segments come back
	// the size the *sending* side chose, which is not this interface's MTU, and
	// a buffer too short for one is a memory fault rather than a short read.
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

	// fresh is the listener's memory of handshake timestamps, and freshClock
	// the dialler's source of them; legacyUntil is when a dialler that met a
	// listener without timestamps tries them again. Under mu. See freshness.go.
	fresh       freshJudge
	freshClock  freshClock
	legacyUntil time.Time

	// unanswered is when the dialling side sent the first packet that nothing
	// has come back after, as unix nanoseconds; zero once anything authentic
	// arrives. See peerSilentAfter.
	unanswered atomic.Int64
	// silentRekey marks the handshake that unanswered started, so it is not
	// reported as a routine rekey.
	silentRekey atomic.Bool

	// The listening side answers a retransmitted first message with the
	// identical reply rather than starting a second handshake, which would
	// derive keys the initiator has no way to arrive at.
	lastInitID uint32
	lastReply  []byte

	// seenInits refuses a handshake this end has already answered, for the
	// legacy handshake an older dialler still sends; a v2 dialler's handshake
	// carries a timestamp that fresh judges instead. See initreplay.go and
	// freshness.go.
	seenInits seenInits

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
	// Whatever the carrier wants said about itself — currently only whether the
	// XDP receive fast path took. It declines silently by design, so this is
	// the only place an operator learns which of the two they got.
	if d, ok := carrier.(interface{ Diag() string }); ok {
		if note := d.Diag(); note != "" {
			t.log.Infof("%s", note)
		}
	}
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

	// One generation of the tunnel lives exactly as long as the carrier and the
	// device opened above. Every goroutine started below watches this context
	// rather than the caller's, so that when any one of them stops they all do
	// and Run can return to be rebuilt.
	//
	// Watching the caller's context instead was a tunnel that never came back.
	// The pumps stop when the carrier or the device they read fails, but
	// handshakeLoop and probeLoop only ever watched ctx — which outlives any
	// number of generations — so they kept running against a carrier that had
	// already been closed, holding Run inside wg.Wait() forever. The restart in
	// cmd/l3.go therefore never fired: the handshake loop went on logging a
	// retry every few seconds, which reads exactly like a tunnel trying to
	// reconnect, while nothing was ever reopened. Only restarting the process
	// brought it back.
	genCtx, endGeneration := context.WithCancel(ctx)
	defer endGeneration()

	// Unblock both pumps when the generation ends. A read on either device
	// blocks indefinitely, and closing is the portable way to interrupt it.
	// Because this now fires when the generation ends rather than only when
	// the caller's context does, a failure on one device also releases the
	// pump blocked on the other, which would otherwise wait on a read that was
	// never going to return.
	go func() {
		<-genCtx.Done()
		carrier.Close()
		tun.Close()
	}()

	var wg sync.WaitGroup

	// A pump that stops ends the generation: whatever it was reading is gone,
	// and nothing else in the tunnel has anything left to do.
	wg.Add(2)
	go func() { defer wg.Done(); defer endGeneration(); t.pumpFromCarrier(genCtx) }()
	go func() { defer wg.Done(); defer endGeneration(); t.pumpFromTUN(genCtx) }()

	if t.cfg.Mode == ModeDial {
		wg.Add(1)
		go func() { defer wg.Done(); t.handshakeLoop(genCtx) }()
	}

	// Both ends probe: each measures what it can send, and sets its own
	// interface. See mtuprobe.go for why that is better than agreeing on one
	// shared figure.
	wg.Add(1)
	go func() { defer wg.Done(); t.probeLoop(genCtx) }()

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
//
// # Why the loads are in this order
//
// Six counters cannot be read in one step, so a snapshot is always slightly
// behind. Behind is fine. *Impossible* is not, and "4 packets, 0 bytes" was
// reachable: the writer bumped packets first, and a reader landing between the
// two lines saw a tunnel that had carried packets containing nothing.
//
// That is not only ugly on a dashboard. bytesIn and bytesOut are what the
// watchdog's stall detector watches, and a direction that reads as frozen for
// an instant is precisely the signal it exists to act on.
//
// The fix is a pair of orderings that have to stay opposite:
//
//   - every writer adds **bytes, then packets**;
//   - this reader loads **packets, then bytes**.
//
// Then a reader that sees P packets knows the bytes for all P were added before
// the counter reached P, and the byte load that follows can only be larger. So
// packets > 0 implies bytes > 0, always, and the snapshot is merely stale
// rather than self-contradictory.
//
// Held by TestStatsAreNeverInternallyImpossible, which found the original by
// reading flat out while packets crossed.
func (t *Tunnel) Stats() Stats {
	packetsIn := t.stats.packetsIn.Load()
	packetsOut := t.stats.packetsOut.Load()
	return Stats{
		PacketsIn:  packetsIn,
		PacketsOut: packetsOut,
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
		bufs[i] = make([]byte, tunReadBuf)
	}
	sizes := make([]int, batch)

	frame := make([]byte, 0, t.cfg.MTU+t.encap.Overhead())

	// Sealed packets, one slot per slot the TUN read filled.
	//
	// Separate buffers rather than one reused for every packet, because a batch
	// send has to hold all of them at once — the single `out` buffer this used
	// worked only because each packet was written before the next was sealed.
	sealedSize := t.cfg.MTU + t.encap.Overhead() + dataOverhead
	sealed := make([][]byte, batch)
	for i := range sealed {
		sealed[i] = make([]byte, 0, sealedSize)
	}
	// The slice handed to the carrier: the first k sealed packets of this
	// round, re-sliced rather than rebuilt.
	ready := make([][]byte, batch)
	// The inner size of each one. The counters have always meant the bytes the
	// tunnel *carried*, not the bytes it put on the wire — reporting the sealed
	// size would make every tunnel look like it was moving more than it was,
	// by exactly its own overhead.
	payloads := make([]int, batch)

	// sendmmsg, when the carrier has it. Only the plain UDP carrier does; the
	// rest keep writing one datagram per syscall exactly as before. See
	// batchread.go.
	writer := asBatchWriter(t.carrier)

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

		// Seal everything this round first, then send it.
		//
		// The two used to be interleaved — seal one, write one — which is why
		// a single sealing buffer sufficed. Separating them is what lets the
		// whole round leave in one syscall, and it costs nothing when the
		// carrier cannot batch: the send loop below is the old one.
		k := 0
		var payload int
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
			out, err := sess.seal(sealed[k][:0], wrapped)
			if err != nil {
				t.stats.dropped.Add(1)
				t.log.Warnf("l3: sealing a packet: %v", err)
				continue
			}
			sealed[k] = out
			ready[k] = out
			payloads[k] = n
			payload += n
			k++
		}
		if k == 0 {
			continue
		}
		t.noteSent()

		if !t.send(ctx, writer, ready[:k], payloads[:k], peer, payload) {
			return
		}
	}
}

// send puts a round of sealed packets on the wire, in one syscall where the
// carrier allows it.
//
// It returns false only when the run is over, so the pump can stop. Everything
// else — a short write, a refused datagram — is a drop, which is what a UDP
// carrier does with them in any case.
func (t *Tunnel) send(ctx context.Context, writer batchWriter, ready [][]byte,
	payloads []int, peer net.Addr, payload int) bool {

	if writer != nil && len(ready) > 1 {
		sent, err := writer.WriteBatch(ready, peer)
		if err == nil {
			t.account(ready, sent, payload)
			return true
		}
		if !errors.Is(err, errNoBatch) {
			if ctx.Err() != nil {
				return false
			}
			t.stats.dropped.Add(uint64(len(ready)))
			t.log.Debugf("l3: sending a batch to %s: %v", peer, err)
			return true
		}
		// The carrier declined to batch after all; fall through and write them
		// one at a time rather than dropping a round over an optimisation.
	}

	for i, p := range ready {
		if _, err := t.carrier.WriteTo(p, peer); err != nil {
			if ctx.Err() != nil {
				return false
			}
			t.stats.dropped.Add(1)
			t.log.Debugf("l3: sending to %s: %v", peer, err)
			continue
		}
		// Bytes before packets. See Stats for the pair of orderings this is
		// half of. The inner size, not len(p): see payloads above.
		t.stats.bytesOut.Add(uint64(payloads[i]))
		t.stats.packetsOut.Add(1)
	}
	return true
}

// account records a batch that went out.
//
// payload is the inner bytes the round carried, which is what the counter has
// always meant — not the sealed size, which includes the tunnel's own overhead
// and would make a tunnel look like it was carrying more than it was.
func (t *Tunnel) account(ready [][]byte, sent, payload int) {
	if sent < len(ready) {
		// The socket buffer filled. The rest are gone, which is what happens to
		// a UDP datagram there is no room for either way.
		t.stats.dropped.Add(uint64(len(ready) - sent))
	}
	if sent <= 0 {
		return
	}
	// Apportioned, because a short write does not say which ones left. Over a
	// round of packets that are all about the same size this is exact enough
	// for a throughput figure, and the packet count is not approximated at all.
	t.stats.bytesOut.Add(uint64(payload * sent / len(ready)))
	t.stats.packetsOut.Add(uint64(sent))
}

// pumpFromCarrier reads datagrams off the carrier and routes them by kind.
func (t *Tunnel) pumpFromCarrier(ctx context.Context) {
	// The receive batch: one buffer per datagram, reused for the life of the
	// pump. A carrier with no batch capability uses only the first.
	batch := 1
	reader := asBatchReader(t.carrier)
	if reader != nil {
		batch = batchSize
	}
	bufs := make([][]byte, batch)
	for i := range bufs {
		bufs[i] = make([]byte, maxMTU+256)
	}
	// One plaintext buffer per slot as well, and the packets of a whole batch
	// go to the interface in one write.
	//
	// They used to go one at a time, which was one syscall per packet and —
	// the larger cost — one trip up the kernel's receive path per packet. The
	// device takes several at once, and with segmentation offload on, the
	// library coalesces consecutive segments of one TCP flow into a single
	// large one before the kernel sees them (GRO). A batch is whatever the
	// carrier read had in hand, so nothing waits for it to fill.
	plains := make([][]byte, batch)
	for i := range plains {
		plains[i] = make([]byte, 0, maxMTU+256)
	}
	pending := make([][]byte, 0, batch)
	sizes := make([]int, batch)
	froms := make([]net.Addr, batch)
	ticker := time.NewTicker(previousGrace / 2)
	defer ticker.Stop()

	// Retiring old sessions is timer work with nothing else to do it, and the
	// receive pump is the one goroutine guaranteed to exist on both sides.
	//
	// It is tied to this pump's own return as well as to the context, so it
	// cannot outlive the generation that started it. Watching the context
	// alone leaked one of these on every restart: the ticker was stopped on
	// the way out, leaving the goroutine parked on a channel that would never
	// fire again until the process ended.
	done := make(chan struct{})
	defer close(done)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case now := <-ticker.C:
				t.retireSessions(now)
			}
		}
	}()

	for {
		got, err := t.receive(reader, bufs, sizes, froms)
		if err != nil {
			if ctx.Err() == nil {
				t.log.Errorf("l3: reading from the carrier: %v", err)
			}
			return
		}
		pending = pending[:0]
		for i := 0; i < got; i++ {
			var inner []byte
			plains[i], inner = t.route(plains[i], bufs[i][:sizes[i]], froms[i])
			if inner != nil {
				pending = append(pending, inner)
			}
		}
		t.writeToDevice(pending)
	}
}

// writeToDevice hands a batch of authenticated inner packets to the interface.
// They were counted as received when they were opened; any the device refuses
// are counted again as drops.
func (t *Tunnel) writeToDevice(pending [][]byte) {
	if len(pending) == 0 {
		return
	}
	n, err := t.tun.Write(pending)
	if err != nil {
		t.stats.dropped.Add(uint64(len(pending)))
		t.log.Debugf("l3: writing to %s: %v", t.cfg.Iface, err)
		return
	}
	if n > 0 && n < len(pending) {
		// More than the staging buffer holds in one call: the rest go now.
		t.writeToDevice(pending[n:])
	}
}

// receive takes the next datagram, or the next several. It is the only place
// that knows the carrier might be batchable, so the loop above reads the same
// either way.
//
// A batch read that fails falls through to the single read rather than killing
// the pump: recvmmsg is an optimisation, and losing it must cost throughput
// rather than the tunnel. The one exception is a real socket error, which the
// single read will hit again and report properly.
func (t *Tunnel) receive(reader batchReader, bufs [][]byte, sizes []int, froms []net.Addr) (int, error) {
	if reader != nil {
		n, err := reader.ReadBatch(bufs, sizes, froms)
		if err == nil {
			return n, nil
		}
		if !errors.Is(err, errNoBatch) {
			return 0, err
		}
	}
	n, from, err := t.carrier.ReadFrom(bufs[0])
	if err != nil {
		return 0, err
	}
	sizes[0], froms[0] = n, from
	return 1, nil
}

// route parses one datagram off the carrier and hands it to whichever half of
// the protocol owns it.
//
// A data packet is not written here: its inner packet comes back, to go to the
// interface with the rest of the batch. It points into plain, so plain must
// not be reused until it has been written.
func (t *Tunnel) route(plain []byte, datagram []byte, from net.Addr) ([]byte, []byte) {
	h, body, err := parseHeader(datagram)
	if err != nil {
		// A stray datagram on an open port: a scanner, a stale peer, or
		// noise. Not worth a log line above debug.
		t.stats.dropped.Add(1)
		return plain, nil
	}

	switch h.kind {
	case typeInit:
		t.handleInit(h, body, from)
	case typeResp:
		t.handleResp(h, body)
	case typeData:
		return t.handleData(plain, h, body, from)
	case typeProbe, typeProbeAck:
		return t.handleProbeMessage(plain, h, body, from), nil
	}
	return plain, nil
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
	// A handshake answered earlier and now arriving again with a *different*
	// one in between is not a retransmission — it is a replay, and answering it
	// would displace whatever session is pending. Refused silently: a peer that
	// genuinely needs a new session picks a new identifier, so there is nothing
	// a real caller loses here.
	if t.seenInits.seen(h.session, time.Now()) {
		t.mu.Unlock()
		t.stats.dropped.Add(1)
		t.log.Debugf("l3: refusing a handshake from %s: session %08x has been answered "+
			"before, so this is a replay rather than a first contact", from, h.session)
		return
	}
	t.mu.Unlock()

	// The counter on a handshake message is the dialler's announced protocol
	// version; every build before this one sent 0 there and ignored it. See
	// version.go.
	sess, reply, fresh, err := respondFresh(t.cfg.Token, h.session, int(h.counter), body, encapID(t.encap))
	if err == nil {
		// Judged only once the handshake has authenticated: a stranger's
		// timestamp is not allowed to move what this end remembers. And
		// refused in silence, like anything else that is not answered — a
		// replay learns nothing, not even that it was recognised.
		t.mu.Lock()
		if why := t.fresh.admit(fresh); why != nil {
			t.mu.Unlock()
			t.stats.dropped.Add(1)
			t.log.Warnf("l3: refusing a handshake from %s: %v", from, why)
			return
		}
		t.mu.Unlock()
	}
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
	// also what promotes the session.
	//
	// Only when no peer is known at all. A legacy handshake carries no
	// freshness of any kind — NNpsk0 has none, and its payload holds the
	// encapsulation and nothing else — so a recorded one stays valid forever
	// and this end cannot tell its replay from a first contact. It used to be enough
	// that no session was CURRENT, and retireSessions clears current after
	// rejectAfterTime: five idle minutes reopened the window on every tunnel,
	// and one replayed datagram from a forged source then pointed this end's
	// outgoing traffic at an address of the attacker's choosing. It could not
	// be read there — the keys need the initiator's ephemeral, which a replay
	// does not carry — but it was not going to the peer either.
	//
	// Pinning it to "no peer has ever been seen" narrows that to the first
	// handshake after a restart, and the first authenticated packet from the
	// real peer corrects it through notePeer. The residual — a replay
	// accepted as that first handshake — is closed by the timestamp between
	// two v2 builds (freshness.go), and stays open only for a legacy dialler.
	if t.current == nil && t.peer == nil {
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
//
// wbuf is the caller's one-element slice for the write, reused for the same
// reason plain is.
func (t *Tunnel) handleData(plain []byte, h header, body []byte, from net.Addr) ([]byte, []byte) {
	sess, isPending := t.sessionFor(h.session)
	if sess == nil {
		t.stats.dropped.Add(1)
		return plain, nil
	}

	opened, err := sess.open(plain, h, body)
	if err != nil {
		t.stats.dropped.Add(1)
		t.log.Debugf("l3: discarding a datagram from %s: %v", from, err)
		return plain, nil
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
	t.noteAnswered()

	inner, err := t.encap.Unwrap(opened)
	if err != nil {
		t.stats.dropped.Add(1)
		t.log.Debugf("l3: discarding a malformed inner packet from %s: %v", from, err)
		return plain, nil
	}
	// Counted before the packet is handed on, not after, and bytes before
	// packets. See Stats for why the order is load-bearing.
	//
	// Counting first also matches what the name claims. A packet that arrived,
	// authenticated and decrypted *was* received; if the interface then refuses
	// it, that is a drop, and it is counted as one below. Received-and-dropped
	// is a different fact from never-arrived, and only one of them is true here.
	t.stats.bytesIn.Add(uint64(len(inner)))
	t.stats.packetsIn.Add(1)

	return plain, inner
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
		if t.peerSilent(time.Now()) {
			t.log.Warnf("l3: nothing has come back from the peer for %s while this end "+
				"was sending — handshaking again (it may have restarted)", peerSilentAfter)
			t.silentRekey.Store(true)
		}
		if t.silentRekey.Load() || t.needsSession() {
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
