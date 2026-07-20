package metrics

import (
	"testing"
)

// The peer address exists because a datagram listener is one unconnected socket
// and the kernel therefore cannot say who is on the other end. If it is stale
// or missing, the panel shows the wrong thing for a KCP tunnel — which is the
// bug this was added to fix.

func TestPeerIsEmptyUntilReported(t *testing.T) {
	ClearPeer()
	if got := currentPeer(); got != "" {
		t.Errorf("currentPeer() = %q before anything was reported, want empty", got)
	}
}

func TestReportedPeerAppearsInTheSnapshot(t *testing.T) {
	ClearPeer()
	t.Cleanup(ClearPeer)

	ReportPeer("203.0.113.9:54321")

	c := NewCollector(t.TempDir(), "kcp-tunnel", "kcp", "server", nil, nil)
	if got := c.Snapshot().Peer; got != "203.0.113.9:54321" {
		t.Errorf("snapshot peer = %q, want the reported address", got)
	}
}

// A dropped control channel must clear it. Reporting a peer that disconnected
// ten minutes ago as current is worse than reporting nothing, because the panel
// would show a live ping to a link that is down.
func TestClearedPeerLeavesTheSnapshotEmpty(t *testing.T) {
	ReportPeer("203.0.113.9:54321")
	ClearPeer()
	t.Cleanup(ClearPeer)

	c := NewCollector(t.TempDir(), "kcp-tunnel", "kcp", "server", nil, nil)
	if got := c.Snapshot().Peer; got != "" {
		t.Errorf("snapshot peer = %q after ClearPeer, want empty", got)
	}
}

func TestLatestPeerWins(t *testing.T) {
	t.Cleanup(ClearPeer)
	ReportPeer("203.0.113.1:1111")
	ReportPeer("203.0.113.2:2222")

	if got := currentPeer(); got != "203.0.113.2:2222" {
		t.Errorf("currentPeer() = %q, want the most recent address", got)
	}
}

// The peer must not be written for transports that do not need it, or the panel
// would prefer a cached address over the socket table, which is fresher.
func TestPeerIsOmittedFromJSONWhenEmpty(t *testing.T) {
	ClearPeer()
	t.Cleanup(ClearPeer)

	dir := t.TempDir()
	c := NewCollector(dir, "tcp-tunnel", "tcp", "server", nil, nil)
	if err := c.Write(); err != nil {
		t.Fatal(err)
	}

	got, err := Read(dir, "tcp-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer != "" {
		t.Errorf("peer = %q for a tunnel that never reported one", got.Peer)
	}
}

// Reported from the accept path while the collector writes on its own timer.
func TestPeerReportingIsConcurrencySafe(t *testing.T) {
	t.Cleanup(ClearPeer)
	c := NewCollector(t.TempDir(), "kcp-tunnel", "kcp", "server", nil, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			ReportPeer("203.0.113.9:54321")
			ClearPeer()
		}
	}()
	for i := 0; i < 200; i++ {
		_ = c.Snapshot()
	}
	<-done
}
