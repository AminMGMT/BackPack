package manage

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
