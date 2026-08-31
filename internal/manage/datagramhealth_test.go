package manage

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/metrics"
)

// A KCP or UDP tunnel carries no TCP sockets, so anything that decides "is it
// up" by looking for peers in the TCP socket table will call a working tunnel
// offline. That is exactly what the web panel did, and it is an easy mistake to
// make again in a new place, because the wrong answer looks plausible.

func TestDatagramTransportsAreRecognised(t *testing.T) {
	for _, tr := range []string{"kcp", "udp"} {
		if !isDatagram(tr) {
			t.Errorf("%s should be treated as a datagram transport", tr)
		}
	}
	for _, tr := range []string{"tcp", "tcpmux", "ws", "wsmux", "wss", "wssmux"} {
		if isDatagram(tr) {
			t.Errorf("%s is not a datagram transport", tr)
		}
	}
}

// A datagram server has nothing to observe: the listener is one unconnected
// socket that keeps no record of who is talking to it. Reporting that as
// "offline" is worse than useless, because it is a working tunnel.
func TestDatagramServerIsNotReportedOffline(t *testing.T) {
	for _, tr := range []string{"kcp", "udp"} {
		tun := Tunnel{Name: "t", Role: "server", Transport: tr, Addr: "[::]:8989"}
		// An empty TCP socket table is the situation that used to produce the
		// wrong answer.
		if !tunnelHealthy(tun, nil) {
			t.Errorf("a running %s server was reported unhealthy with no TCP sockets", tr)
		}
	}
}

// The same tunnel over TCP genuinely is offline with no peer, and must still be
// reported that way — the fix must not make everything look healthy.
func TestTCPServerWithNoPeerIsStillOffline(t *testing.T) {
	tun := Tunnel{Name: "t", Role: "server", Transport: "tcp", Addr: "0.0.0.0:8989"}
	if tunnelHealthy(tun, nil) {
		t.Error("a TCP server with no connected peer should not be reported healthy")
	}
}

// The health detail must not claim more than was actually checked — and must
// not go on disclaiming something that is now checked.
//
// A datagram server used to report "running — a UDP listener cannot report its
// peers", which was honest while nothing knew. The transport records the peer
// now, so repeating that line would be the opposite of honest: it would excuse
// a state the panel can in fact determine, and it was the excuse that left a
// stopped tunnel showing green.
func TestDatagramServerNoLongerDisclaimsWhatItCanCheck(t *testing.T) {
	src, err := os.ReadFile("health.go")
	if err != nil {
		t.Fatalf("cannot read health.go: %v", err)
	}
	if strings.Contains(string(src), "cannot report its peers") {
		t.Error("the health detail still says the peer cannot be reported; it is read from the snapshot now")
	}
}

// The regression guard: the panel must not compute tunnel state itself. It did,
// and that is why a working KCP tunnel showed as offline in the Iran web UI
// while the watchdog — which had the datagram case right — left it alone.
func TestPanelUsesSharedHealth(t *testing.T) {
	src, err := os.ReadFile("../webui/stats.go")
	if err != nil {
		t.Skipf("cannot read the panel source: %v", err)
	}
	body := string(src)

	if !strings.Contains(body, "manage.AllHealth()") {
		t.Error("the panel does not use manage.AllHealth — it is deciding tunnel state on its own again")
	}
	// The old logic keyed "offline" off an empty TCP peer list.
	if strings.Contains(body, "case len(peers) == 0:") {
		t.Error("the panel still decides offline from an empty TCP peer list, which is wrong for KCP and UDP")
	}
}

// A datagram server kept showing green in the panel after its client had been
// stopped from the other end. The peer and the ping both disappeared, because
// those are read from the tunnel's own snapshot, which knew; the light stayed
// on, because the state came from the socket table, which cannot know.
//
// The two answers now come from different places on purpose. tunnelHealthy is
// the watchdog's question — "is this worth restarting?" — and still says yes
// for a datagram server, because restarting something whose peer cannot be
// observed would mean restarting it forever. The panel's question is "is a peer
// connected", and that is answered from the snapshot.

// stageSnapshot writes a tunnel snapshot into a temporary config directory.
func stageSnapshot(t *testing.T, name, peer string, taken time.Time) string {
	t.Helper()
	dir := t.TempDir()
	body, err := json.Marshal(metrics.Snapshot{Name: name, Peer: peer, Taken: taken})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metrics.Path(dir, name), body, 0644); err != nil {
		t.Fatalf("staging the snapshot: %v", err)
	}
	return dir
}

func TestDatagramServerWithNoPeerReportsOffline(t *testing.T) {
	for _, tr := range []string{"kcp", "udp"} {
		t.Run(tr, func(t *testing.T) {
			dir := stageSnapshot(t, "t", "", time.Now())

			connected, known := datagramPeer(dir, "t")
			if !known {
				t.Fatal("a fresh snapshot was treated as unreadable")
			}
			if connected {
				t.Error("a cleared peer was read as a live connection — the panel would stay green")
			}
		})
	}
}

func TestDatagramServerWithAPeerReportsOnline(t *testing.T) {
	dir := stageSnapshot(t, "t", "203.0.113.9:41234", time.Now())

	connected, known := datagramPeer(dir, "t")
	if !known || !connected {
		t.Errorf("a reported peer was not read back: connected=%v known=%v", connected, known)
	}
}

// Not knowing has to stay separate from knowing there is nobody there, or a
// tunnel that has only just started — and has not written a snapshot yet —
// shows as down for its first half minute.
func TestUnknownIsNotTheSameAsDisconnected(t *testing.T) {
	dir := stageSnapshot(t, "other", "", time.Now()) // nothing for "t"

	if _, known := datagramPeer(dir, "t"); known {
		t.Error("a missing snapshot was treated as a definite answer")
	}

	// The same goes for a snapshot too old to describe now.
	dir = stageSnapshot(t, "t", "203.0.113.9:41234", time.Now().Add(-10*time.Minute))
	if _, known := datagramPeer(dir, "t"); known {
		t.Error("a stale snapshot was treated as current")
	}
}

// The watchdog must keep its old answer. Restarting a datagram server whenever
// its client is away would restart a perfectly good tunnel on a schedule.
func TestWatchdogStillNeverRestartsADatagramServer(t *testing.T) {
	stageSnapshot(t, "t", "", time.Now()) // no peer: the panel now calls this offline

	for _, tr := range []string{"kcp", "udp"} {
		tun := Tunnel{Name: "t", Role: "server", Transport: tr, Addr: "[::]:8989"}
		if !tunnelHealthy(tun, nil) {
			t.Errorf("%s server reported unhealthy to the watchdog; it would be restarted in a loop", tr)
		}
	}
}

// The carriers that exist to leave nothing observable behind are the ones the
// socket table cannot answer for, on either end.
//
// This is the regression test for a tunnel that carried traffic while one
// machine's card showed offline and the other's showed online. The listening
// side had been taught to write its peer into the snapshot and read it back;
// the dialling side was still left to the socket table. That works for the
// carriers that dial a connected UDP socket — plain kcp, udp, quic — and finds
// nothing for xdi, pck and spoof, which is exactly the point of them.
//
// Spoof appears here as a direct carrier ("l3/spoof") rather than a reverse
// transport, which is the only shape it has now: a reverse tunnel over a forged
// source could never carry traffic and is refused at load.
func TestObservabilityFreeCarriersCountAsDatagram(t *testing.T) {
	for _, tr := range []string{"xdi", "pck", "l3/spoof", "kcp", "udp", "quic"} {
		if !isDatagram(tr) {
			t.Errorf("%s must be judged from the snapshot, not the socket table", tr)
		}
	}
}

// The snapshot answers for either end. Nothing in it is about a role, and the
// panel now asks it for both — a client that reports a peer is connected, and
// one that has cleared it is not.
func TestTheSnapshotAnswersForTheDiallingSideToo(t *testing.T) {
	t.Run("a client that reported its peer is online", func(t *testing.T) {
		dir := stageSnapshot(t, "t", "198.51.100.7:8443", time.Now())
		connected, known := datagramPeer(dir, "t")
		if !known || !connected {
			t.Errorf("a dialling side's peer was not read back: connected=%v known=%v", connected, known)
		}
	})

	t.Run("a client whose control channel dropped is offline", func(t *testing.T) {
		dir := stageSnapshot(t, "t", "", time.Now())
		connected, known := datagramPeer(dir, "t")
		if !known {
			t.Fatal("a fresh snapshot was treated as unreadable")
		}
		if connected {
			t.Error("a cleared peer was read as a live connection")
		}
	})
}

// The watchdog's answer for a datagram server must not have moved: restarting
// something whose peer cannot be observed would mean restarting it forever.
func TestExtendingTheSnapshotCheckLeftTheWatchdogAlone(t *testing.T) {
	stageSnapshot(t, "t", "", time.Now())

	// The reverse carriers only: a direct tunnel is judged by directHealthy,
	// which has its own test.
	for _, tr := range []string{"xdi", "pck"} {
		tun := Tunnel{Name: "t", Role: "server", Transport: tr, Addr: "[::]:8989"}
		if !tunnelHealthy(tun, nil) {
			t.Errorf("%s server reported unhealthy to the watchdog; it would be restarted in a loop", tr)
		}
	}
}
