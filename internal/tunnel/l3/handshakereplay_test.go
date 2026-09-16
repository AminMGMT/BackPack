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
