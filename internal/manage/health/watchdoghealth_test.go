package health

import (
	"testing"
	"time"

	"github.com/backpack/backpack/internal/metrics"
)

// The watchdog has to ask the engine, not the kernel.
//
// It judged a tunnel by whether `ss` showed an ESTABLISHED socket, and a socket
// outlives the tunnel behind it by a long way: one whose keepalive probes go
// unanswered stays ESTABLISHED for about eleven minutes on the shipped
// defaults, and one stalled on a path that drops full-sized packets stays
// ESTABLISHED while it retransmits. Both were reported this week as "the tunnel
// is down and I have to restart it by hand" — which is precisely the job the
// watchdog exists to do and could not, because the only thing it looked at said
// everything was fine.
// A snapshot with no opinion must not be read as "not connected".
//
// Through an update the binary is replaced while the tunnels keep running the
// previous one, so for a few minutes the watchdog is new and the snapshots are
// old. Reading a missing field as false there would restart every tunnel on the
// host at once — the update would look like an outage.
func TestASnapshotWithNoOpinionFallsBackToTheSocketTable(t *testing.T) {
	if connected, known := engineSaysConnected("no-such-tunnel"); known || connected {
		t.Fatalf("a tunnel with no snapshot reported known=%v connected=%v; it has to "+
			"be unknown so the caller falls back", known, connected)
	}
}

// A stale snapshot means the engine has stopped writing, which says nothing
// about the tunnel either way.
func TestAStaleSnapshotIsNotAnAnswer(t *testing.T) {
	yes := true
	old := metrics.Snapshot{
		Name:      "stale",
		Taken:     time.Now().Add(-datagramPeerWindow - time.Minute),
		Connected: &yes,
	}
	if old.Connected == nil {
		t.Fatal("setup")
	}
	if time.Since(old.Taken) <= datagramPeerWindow {
		t.Fatal("setup: the snapshot is not actually stale")
	}
	// engineSaysConnected drops it for age before it ever reads Connected.
	if connected, known := engineSaysConnected("stale"); known || connected {
		t.Errorf("a stale snapshot answered known=%v connected=%v", known, connected)
	}
}

// The tri-state is the whole point: absent, true and false must be three
// different things, or the upgrade window becomes an outage.
func TestConnectedIsATriState(t *testing.T) {
	var absent metrics.Snapshot
	if absent.Connected != nil {
		t.Error("a zero snapshot claims to know whether it is connected")
	}

	metrics.ClearPeer()
	if got := metrics.SnapshotConnected(); got == nil || *got {
		t.Errorf("after ClearPeer the engine reports %v, want a definite false", got)
	}
	metrics.ReportPeer("198.51.100.9:443")
	if got := metrics.SnapshotConnected(); got == nil || !*got {
		t.Errorf("after ReportPeer the engine reports %v, want a definite true", got)
	}
	metrics.ClearPeer()
}

// One snapshot read twice is one failure, not the two in a row that restart a
// tunnel. The watchdog looks every 25 seconds and a snapshot is written every
// 30, so it reads most of them twice; counting each read restarted a pck tunnel
// that was between sessions for a second at the moment its snapshot was taken.
func TestASnapshotReadTwiceIsOneFailure(t *testing.T) {
	counted := map[string]time.Time{}
	at := time.Now().Add(-10 * time.Second)
	if !newFailure(counted, "t", at) {
		t.Fatal("the first reading of a down snapshot was not counted")
	}
	if newFailure(counted, "t", at) {
		t.Fatal("the same snapshot, read again 25 seconds later, counted as a second failure")
	}
	if !newFailure(counted, "t", at.Add(30*time.Second)) {
		t.Fatal("the next snapshot, still down, was not counted")
	}
	if !newFailure(counted, "other", at) {
		t.Fatal("a reading of one tunnel kept another's from counting")
	}
	// A verdict from the socket table is read fresh on every pass.
	if !newFailure(counted, "socket", time.Time{}) || !newFailure(counted, "socket", time.Time{}) {
		t.Fatal("a fresh reading with no snapshot behind it was not counted every time")
	}
}
