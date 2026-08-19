package l3

import (
	"net"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/sirupsen/logrus"
)

// Retiring sessions, and what the management screens read from it.
//
// A layer-3 tunnel holds no socket the kernel can report on: udp is an
// unconnected socket and the raw carriers do not go through the stack at all.
// So the engine writes its peer down, and the health screen reads that. Which
// makes retiring a session not merely housekeeping — it is the only thing that
// can ever say the tunnel is down.

// tunnelWithSession builds a tunnel with a current session created at a chosen
// time, without opening anything.
func tunnelWithSession(t *testing.T, age time.Duration) *Tunnel {
	t.Helper()
	tunnel, err := New(Config{
		Mode: ModeListen, Addr: "0.0.0.0:9000", Token: "token",
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2",
	}, logrus.New())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tunnel.current = &session{id: 1, created: time.Now().Add(-age)}
	tunnel.peer = &net.UDPAddr{IP: net.ParseIP("203.0.113.9"), Port: 9000}
	return tunnel
}

// A session past rejectAfterTime must be dropped, current included.
//
// It was not, and only previous and pending ever were. A tunnel whose peer had
// gone away therefore held its last session for as long as the process lived —
// and with it the peer in the metrics snapshot, so the panel went on showing
// "peer connected" for a tunnel that had been down for hours. That is the exact
// thing the snapshot was added to prevent.
func TestAnExpiredSessionIsRetired(t *testing.T) {
	metrics.ClearPeer()
	t.Cleanup(metrics.ClearPeer)

	tunnel := tunnelWithSession(t, rejectAfterTime+time.Second)
	tunnel.publishPeer()
	if metrics.SnapshotPeer() == "" {
		t.Fatal("the peer was not published, so this test cannot show it being cleared")
	}

	tunnel.retireSessions(time.Now())

	if tunnel.current != nil {
		t.Error("a session past rejectAfterTime is still current")
	}
	if peer := metrics.SnapshotPeer(); peer != "" {
		t.Errorf("the panel would still show %q for a tunnel with no session", peer)
	}
}

// A session inside its lifetime must be left alone, or a working tunnel would
// be torn down every fifteen seconds.
func TestALiveSessionSurvivesRetirement(t *testing.T) {
	metrics.ClearPeer()
	t.Cleanup(metrics.ClearPeer)

	tunnel := tunnelWithSession(t, rekeyAfterTime) // due for rekey, not expired
	tunnel.publishPeer()

	tunnel.retireSessions(time.Now())

	if tunnel.current == nil {
		t.Error("a session that is merely due for rekey was retired")
	}
	if metrics.SnapshotPeer() == "" {
		t.Error("the peer was cleared for a tunnel that still has a session")
	}
}

// The dialling side must go back and get another one, or expiring the session
// would take the tunnel down rather than replacing it.
func TestAnExpiredSessionIsAskedForAgain(t *testing.T) {
	tunnel := tunnelWithSession(t, rejectAfterTime+time.Second)
	tunnel.cfg.Mode = ModeDial

	if !tunnel.needsSession() {
		t.Fatal("an expired session is not asked for again")
	}
	tunnel.retireSessions(time.Now())
	if !tunnel.needsSession() {
		t.Fatal("a tunnel with no session at all is not asked for one")
	}
}
