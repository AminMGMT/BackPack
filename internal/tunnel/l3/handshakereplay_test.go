package l3

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/metrics"
)

// A second, unconfirmed handshake must not move the peer a first one
// established.
//
// The l3 handshake is NNpsk0 and carries no freshness of any kind — the init
// payload holds the encapsulation and nothing else — so a recorded typeInit
// datagram stays valid for ever and the listening side cannot tell a replay
// from a first contact. handleInit used to adopt the source of any accepted
// init as the peer whenever no session was CURRENT, and retireSessions clears
// current after rejectAfterTime: five idle minutes reopened that window on
// every tunnel. One replayed datagram from a forged source then pointed this
// end's outgoing packets at an address of the attacker's choosing. They could
// not be read there — the keys need the initiator's ephemeral, which a replay
// does not carry — but they were not reaching the peer either, and the panel
// named the attacker as the connected peer.
//
// Pinning it to "no peer has ever been seen" leaves the first handshake after a
// restart working exactly as before and closes every later one. The rest is the
// protocol's to fix: a monotonic timestamp in the init payload, refused unless
// it advances, which is a wire change both ends have to agree on.
func TestASecondHandshakeDoesNotMoveAnEstablishedPeer(t *testing.T) {
	metrics.ClearPeer()
	t.Cleanup(metrics.ClearPeer)

	const token = "a-token-worth-replaying"
	dev := newFakeDevice(1400)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	listener, err := New(Config{
		Mode: ModeListen, Addr: "127.0.0.1:0", Token: token, Encap: "gre",
		LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1400,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(listener): %v", err)
	}
	listener.openDevice = func(deviceSpec) (packetDevice, error) { return dev, nil }
	start(t, ctx, cancel, listener, dev)
	bound := awaitBind(t, listener)

	// The genuine peer.
	genuine, err := net.Dial("udp", bound.String())
	if err != nil {
		t.Fatalf("dial genuine: %v", err)
	}
	defer genuine.Close()
	first, err := beginHandshake(token, 0, "gre")
	if err != nil {
		t.Fatalf("beginHandshake: %v", err)
	}
	if _, err := genuine.Write(first.datagram()); err != nil {
		t.Fatalf("write genuine: %v", err)
	}
	wantPeer := awaitPeer(t, "")
	if wantPeer != genuine.LocalAddr().String() {
		t.Fatalf("the listener reported %q, want the genuine peer %q", wantPeer, genuine.LocalAddr())
	}

	// Somebody else, from a different address, with a handshake this end has
	// no way to distinguish from a first contact. No data packet follows it,
	// so nothing ever confirms it.
	intruder, err := net.Dial("udp", bound.String())
	if err != nil {
		t.Fatalf("dial intruder: %v", err)
	}
	defer intruder.Close()
	second, err := beginHandshake(token, first.id, "gre")
	if err != nil {
		t.Fatalf("beginHandshake(second): %v", err)
	}
	if _, err := intruder.Write(second.datagram()); err != nil {
		t.Fatalf("write intruder: %v", err)
	}

	// Long enough that the listener has certainly processed it — it answered
	// the first one in well under this.
	time.Sleep(300 * time.Millisecond)

	if got := metrics.SnapshotPeer(); got != wantPeer {
		t.Fatalf("an unconfirmed handshake moved the peer to %q; it should still be %q",
			got, wantPeer)
	}
}

// awaitPeer waits for the reported peer to become something other than unwanted.
func awaitPeer(t *testing.T, unwanted string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if p := metrics.SnapshotPeer(); p != unwanted {
			return p
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the listener never reported a peer")
	return ""
}

// A handshake answered once is not answered again.
//
// The protocol carries no freshness, so a recorded typeInit stays valid for
// ever and the responder cannot tell a replay from a first contact. The proper
// fix is a timestamp and it needs a version both ends understand — see
// initreplay.go for why that cannot ship in the same release as the version
// mechanism itself.
//
// What can ship is this: the initiator picks a random identifier per handshake
// and it already travels in the header, so remembering the ones recently
// answered refuses a repeat with no wire change at all. It does not close the
// hole. It closes the attack — one recorded packet replayed over and over to
// keep a tunnel from establishing now costs the attacker a packet and buys
// nothing.
func TestAHandshakeAnsweredOnceIsNotAnsweredAgain(t *testing.T) {
	var s seenInits
	now := time.Now()

	if s.seen(0xAABBCCDD, now) {
		t.Fatal("the first sighting of an identifier was reported as a repeat")
	}
	if !s.seen(0xAABBCCDD, now) {
		t.Error("the same identifier was accepted twice; a replay would be answered")
	}
	// A different one is a genuine new handshake and must still get through.
	if s.seen(0x11223344, now) {
		t.Error("a different identifier was refused as a replay")
	}
}

// The memory is bounded in both directions: by count, so a long-lived tunnel
// does not accumulate, and by time, so an identifier is eventually forgotten
// rather than refusing a genuine handshake for ever.
func TestTheHandshakeMemoryIsBounded(t *testing.T) {
	var s seenInits
	now := time.Now()

	// Past the count limit: the earliest is forgotten and would be accepted
	// again, which is the deliberate limit of this approach.
	for i := 0; i < initMemory+10; i++ {
		s.seen(uint32(i+1), now)
	}
	if len(s.ids) > initMemory {
		t.Errorf("the memory holds %d entries, cap is %d", len(s.ids), initMemory)
	}
	if !s.seen(uint32(initMemory+10), now) {
		t.Error("the most recent identifier was forgotten")
	}

	// Past the time window: everything ages out, so a tunnel up for months does
	// not carry a set that only grows.
	var s2 seenInits
	s2.seen(0x99999999, now)
	later := now.Add(initMemoryWindow + time.Minute)
	if s2.seen(0x99999999, later) {
		t.Error("an identifier older than the window was still refused; the memory " +
			"never forgets and a genuine handshake reusing an old identifier would " +
			"be refused for ever")
	}
}

// The timestamp switch is deliberately off. It is written and tested so that
// turning it on later is a one-line change rather than a design problem, and
// off because an initiator cannot put a timestamp in its first message without
// being refused by every listener already in the field.
func TestTheFreshnessSwitchIsOffUntilVersionTwoExists(t *testing.T) {
	for v := versionLegacy; v <= versionCurrent; v++ {
		if freshnessRequired(v) {
			t.Errorf("freshness is required at v%d, but no version this build speaks "+
				"carries a timestamp — every handshake would be refused", v)
		}
	}
	if !freshnessRequired(2) {
		t.Error("freshnessRequired(2) is false; the switch has nothing to turn on")
	}
}
