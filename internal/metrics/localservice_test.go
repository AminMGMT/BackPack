package metrics

import (
	"testing"
	"time"
)

// The last hop is recorded, not just logged.
//
// A tunnel can be entirely healthy and carry nothing: the client delivers each
// connection to the service it forwards to, and if nothing is listening there
// every one of them dies one step past the end of the tunnel. The control
// channel stays up, the peer is reported, and the only account of the failure
// used to be a line in the client's log — on a machine the operator is not
// looking at.
func TestAFailingLastHopIsInTheSnapshot(t *testing.T) {
	t.Cleanup(ReportLocalDialSuccess)
	ReportLocalDialSuccess()

	if SnapshotLocalService() != nil {
		t.Fatal("a tunnel that has dialled nothing already claims its far service is down")
	}

	ReportLocalDialFailure("127.0.0.1:9862", "refused")
	got := SnapshotLocalService()
	if got == nil {
		t.Fatal("a refused dial to the forwarded service left no record")
	}
	if got.Addr != "127.0.0.1:9862" || got.Why != "refused" {
		t.Errorf("the record does not name what failed: %+v", got)
	}
	if got.Failures != 1 {
		t.Errorf("failures = %d, want 1", got.Failures)
	}
}

// Repeats accumulate. "refused four hundred connections since 15:13" and "one
// refusal" are different readings — the first is a service that is down, the
// second is a connection that lost a race while something restarted.
func TestRepeatedRefusalsAccumulate(t *testing.T) {
	t.Cleanup(ReportLocalDialSuccess)
	ReportLocalDialSuccess()

	ReportLocalDialFailure("127.0.0.1:9862", "refused")
	first := SnapshotLocalService().Since
	for i := 0; i < 9; i++ {
		ReportLocalDialFailure("127.0.0.1:9862", "refused")
	}
	got := SnapshotLocalService()
	if got.Failures != 10 {
		t.Errorf("ten refusals recorded as %d — a run that does not count is a run "+
			"that cannot be told from a single failure", got.Failures)
	}
	if !got.Since.Equal(first) {
		t.Error("the start of the run moved, so its age is always about a second")
	}
}

// A different address starts its own run, because it is a different fact.
func TestADifferentAddressStartsItsOwnRun(t *testing.T) {
	t.Cleanup(ReportLocalDialSuccess)
	ReportLocalDialSuccess()

	ReportLocalDialFailure("127.0.0.1:9862", "refused")
	ReportLocalDialFailure("127.0.0.1:9862", "refused")
	ReportLocalDialFailure("127.0.0.1:443", "refused")

	got := SnapshotLocalService()
	if got.Addr != "127.0.0.1:443" || got.Failures != 1 {
		t.Errorf("a new address inherited the old one's count: %+v", got)
	}
}

// One connection landing ends the run. The operator's fix has to show.
func TestOneSuccessfulDialClearsIt(t *testing.T) {
	t.Cleanup(ReportLocalDialSuccess)

	ReportLocalDialFailure("127.0.0.1:9862", "refused")
	ReportLocalDialSuccess()
	if got := SnapshotLocalService(); got != nil {
		t.Errorf("the far service answered and the tunnel still reports it down: %+v", got)
	}
}

// And it reaches the written snapshot, which is what health reads.
func TestTheSnapshotCarriesIt(t *testing.T) {
	t.Cleanup(ReportLocalDialSuccess)
	ReportLocalDialSuccess()

	c := &Collector{name: "test", transport: "tcp", role: "client", started: time.Now()}
	if s := c.Snapshot(); s.LocalService != nil {
		t.Error("a healthy tunnel's snapshot carries a claim about its far service")
	}

	ReportLocalDialFailure("127.0.0.1:9862", "refused")
	s := c.Snapshot()
	if s.LocalService == nil || s.LocalService.Addr != "127.0.0.1:9862" {
		t.Errorf("the snapshot does not carry the failing last hop: %+v", s.LocalService)
	}
}
