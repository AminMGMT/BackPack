package l3

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

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

// errFlowStuck is Run's answer when the generation ended because its flow
// stopped answering; the caller opens a new one, from new source ports.
var errFlowStuck = errors.New("l3: the carrier's flow stopped answering handshakes — reopening it from new source ports")

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

	// badHandshakes paces the warning about handshakes that do not
	// authenticate. See handleInit.
	badHandshakes reportEvery

	// foreignTags paces the warning about xdi echoes carrying another
	// tunnel's tag. See noteForeignTag.
	foreignTags reportEvery

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

	// flowStuck is set by the handshake loop when it ends a generation because
	// a pck or sni flow stopped answering (see flowStuckAfter), so Run can say
	// why it returned.
	flowStuck atomic.Bool

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
		cfg:           cfg,
		encap:         encap,
		log:           log,
		replies:       make(chan handshakeReply, 4),
		mtuCurrent:    cfg.MTU,
		probe:         defaultProbeTiming(),
		probeWaiters:  make(map[uint32]chan uint32),
		badHandshakes: reportEvery{every: time.Minute},
		foreignTags:   reportEvery{every: time.Minute},
	}, nil
}

// Run opens the device and the carrier and serves the tunnel until ctx ends.
// It always cleans up what it opened, including on the error paths.
func (t *Tunnel) Run(ctx context.Context) error {
	carrier, peer, err := openCarrierContext(ctx, t.cfg)
	if err != nil {
		return err
	}
	if h, ok := carrier.(foreignTagHooker); ok {
		h.SetForeignHook(t.noteForeignTag)
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
	if w, ok := carrier.(interface{ Warning() string }); ok {
		if warning := w.Warning(); warning != "" {
			t.log.Warnf("%s", warning)
		}
	}
	t.setPeer(peer)
	t.localAddrMu.Lock()
	t.localAddr = carrier.LocalAddr()
	t.localAddrMu.Unlock()
	defer carrier.Close()
	if err := ctx.Err(); err != nil {
		return err
	}

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
		go func() { defer wg.Done(); t.handshakeLoop(genCtx, endGeneration) }()
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
	if t.flowStuck.Swap(false) {
		return errFlowStuck
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
