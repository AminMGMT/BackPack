package control

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/backpack/backpack/internal/node"
)

// The comparison is the whole feature, so it is tested without a fleet: every
// case below is a thing that has actually happened to somebody's servers, and
// none of them needs a machine to reproduce.

func testDesired(t *testing.T) *Desired {
	t.Helper()
	return &Desired{
		path:  filepath.Join(t.TempDir(), "desired.json"),
		nodes: map[string]map[string]TunnelIntent{},
	}
}

func record(t *testing.T, d *Desired, node string, ti TunnelIntent) {
	t.Helper()
	if err := d.Record(node, ti); err != nil {
		t.Fatalf("recording intent: %v", err)
	}
}

func kinds(drifts []Drift) map[string]DriftKind {
	out := map[string]DriftKind{}
	for _, d := range drifts {
		out[d.Tunnel] = d.Kind
	}
	return out
}

// Nothing to report is the ordinary case and has to be silent, or the report is
// noise and nobody reads the one line that matters.
func TestAFleetThatMatchesReportsNothing(t *testing.T) {
	d := testDesired(t)
	record(t, d, "de-1", TunnelIntent{Name: "fr-relay", Role: "client", TunnelPort: "8443", Running: true})
	record(t, d, "de-1", TunnelIntent{Name: "nl-relay", Role: "client", TunnelPort: "9443", Running: true})

	drifts := d.Reconcile("de-1", []node.TunnelState{
		{Name: "fr-relay", Role: "client", TunnelPort: "8443", Active: true},
		{Name: "nl-relay", Role: "client", TunnelPort: "9443", Active: true},
	})
	if len(drifts) != 0 {
		t.Fatalf("a fleet that matches reported %d differences: %+v", len(drifts), drifts)
	}
}

// The case the whole thing exists for: somebody with a terminal removed a
// tunnel the panel created, and until now nothing ever found out.
func TestATunnelThatWasDeletedOnTheMachineIsFound(t *testing.T) {
	d := testDesired(t)
	record(t, d, "de-1", TunnelIntent{Name: "fr-relay", Running: true})

	drifts := d.Reconcile("de-1", nil)
	if len(drifts) != 1 || drifts[0].Kind != DriftMissing {
		t.Fatalf("a deleted tunnel came back as %+v", drifts)
	}
	if drifts[0].Node != "de-1" || drifts[0].Tunnel != "fr-relay" {
		t.Fatalf("the report does not name what is missing: %+v", drifts[0])
	}
}

// A unit disabled during an incident and never re-enabled.
func TestATunnelThatIsMeantToRunAndIsNotIsFound(t *testing.T) {
	d := testDesired(t)
	record(t, d, "de-1", TunnelIntent{Name: "fr-relay", Running: true})

	drifts := d.Reconcile("de-1", []node.TunnelState{{Name: "fr-relay", Active: false}})
	if got := kinds(drifts)["fr-relay"]; got != DriftStopped {
		t.Fatalf("a stopped tunnel came back as %q: %+v", got, drifts)
	}
}

// And the other way: a tunnel deliberately stopped from the panel must not be
// reported for ever afterwards, or the report becomes something to ignore.
func TestATunnelStoppedOnPurposeIsNotDrift(t *testing.T) {
	d := testDesired(t)
	record(t, d, "de-1", TunnelIntent{Name: "fr-relay", Running: true})
	if err := d.SetRunning("de-1", "fr-relay", false); err != nil {
		t.Fatalf("recording the stop: %v", err)
	}

	if drifts := d.Reconcile("de-1", []node.TunnelState{{Name: "fr-relay", Active: false}}); len(drifts) != 0 {
		t.Fatalf("a tunnel stopped on purpose was reported: %+v", drifts)
	}
	// Started again behind the panel's back, though, is worth saying.
	drifts := d.Reconcile("de-1", []node.TunnelState{{Name: "fr-relay", Active: true}})
	if got := kinds(drifts)["fr-relay"]; got != DriftRunning {
		t.Fatalf("a tunnel started behind the panel came back as %q", got)
	}
}

// A tunnel under the same name that is not the same tunnel. This matters more
// than a stopped one: the two ends no longer meet, and both of them look fine
// from their own side.
func TestATunnelThatNoLongerMatchesWhatWasWrittenIsFound(t *testing.T) {
	d := testDesired(t)
	record(t, d, "de-1", TunnelIntent{Name: "fr-relay", Role: "client", TunnelPort: "8443", Running: true})

	drifts := d.Reconcile("de-1", []node.TunnelState{
		{Name: "fr-relay", Role: "client", TunnelPort: "9999", Active: true},
	})
	if got := kinds(drifts)["fr-relay"]; got != DriftChanged {
		t.Fatalf("a changed port came back as %q: %+v", got, drifts)
	}

	drifts = d.Reconcile("de-1", []node.TunnelState{
		{Name: "fr-relay", Role: "server", TunnelPort: "8443", Active: true},
	})
	if got := kinds(drifts)["fr-relay"]; got != DriftChanged {
		t.Fatalf("a swapped role came back as %q: %+v", got, drifts)
	}
}

// A field the node does not report is not a difference. An older node that
// answers without a role or a port would otherwise show every tunnel as
// changed, which is how a useful report becomes one nobody opens.
func TestAFieldTheNodeDoesNotReportIsNotADifference(t *testing.T) {
	d := testDesired(t)
	record(t, d, "de-1", TunnelIntent{Name: "fr-relay", Role: "client", TunnelPort: "8443", Running: true})

	if drifts := d.Reconcile("de-1", []node.TunnelState{{Name: "fr-relay", Active: true}}); len(drifts) != 0 {
		t.Fatalf("a node that reports less was read as drift: %+v", drifts)
	}
}

// A tunnel the panel did not create is named rather than ignored — it is how a
// hand-made one, or one left by a panel restored from a backup, appears — but
// it is not a fault and the wording says so.
func TestATunnelThePanelDidNotCreateIsNamed(t *testing.T) {
	d := testDesired(t)
	record(t, d, "de-1", TunnelIntent{Name: "fr-relay", Running: true})

	drifts := d.Reconcile("de-1", []node.TunnelState{
		{Name: "fr-relay", Active: true},
		{Name: "made-by-hand", Active: true},
	})
	if got := kinds(drifts)["made-by-hand"]; got != DriftUnexpected {
		t.Fatalf("an unknown tunnel came back as %q: %+v", got, drifts)
	}
	if len(drifts) != 1 {
		t.Fatalf("the tunnel that does match was also reported: %+v", drifts)
	}
}

// Deleting a tunnel through the panel is not drift the next time anybody looks.
func TestForgettingATunnelStopsItBeingExpected(t *testing.T) {
	d := testDesired(t)
	record(t, d, "de-1", TunnelIntent{Name: "fr-relay", Running: true})
	if err := d.Forget("de-1", "fr-relay"); err != nil {
		t.Fatalf("forgetting: %v", err)
	}
	if drifts := d.Reconcile("de-1", nil); len(drifts) != 0 {
		t.Fatalf("a tunnel deleted through the panel was still expected: %+v", drifts)
	}
	if len(d.Nodes()) != 0 {
		t.Fatalf("a node with nothing expected of it is still listed: %v", d.Nodes())
	}
}

// The intent survives a restart, which is the only reason to write it down.
func TestTheIntentSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "desired.json")

	first := &Desired{path: path, nodes: map[string]map[string]TunnelIntent{}}
	record(t, first, "de-1", TunnelIntent{Name: "fr-relay", Role: "client", TunnelPort: "8443", Running: true})

	second := &Desired{path: path, nodes: map[string]map[string]TunnelIntent{}}
	second.load()

	got := second.Intent("de-1")
	if len(got) != 1 || got[0].Name != "fr-relay" || got[0].TunnelPort != "8443" {
		t.Fatalf("the intent did not survive: %+v", got)
	}
	if got[0].Recorded == 0 {
		t.Error("the intent has no timestamp, so a report cannot say how old the expectation is")
	}
}

// A panel that cannot read its own intent has to come up anyway. An intent
// nobody can read makes every node look unexpected, which is noise; a panel
// that refuses to start makes the fleet unreachable, and the fleet is how
// somebody would fix it.
func TestAnUnreadableIntentDoesNotStopThePanel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desired.json")
	if err := os.WriteFile(path, []byte("this is not json"), 0o600); err != nil {
		t.Fatalf("writing the broken file: %v", err)
	}
	d := &Desired{path: path, nodes: map[string]map[string]TunnelIntent{}}
	d.load()
	if len(d.Nodes()) != 0 {
		t.Fatalf("a broken intent produced %v", d.Nodes())
	}
	// And it can still be written to afterwards.
	record(t, d, "de-1", TunnelIntent{Name: "fr-relay", Running: true})
	if len(d.Intent("de-1")) != 1 {
		t.Fatal("the intent could not be rebuilt after an unreadable file")
	}
}

// Starting or stopping a tunnel the panel did not create records nothing. The
// panel drives tunnels it did not make, and inventing an intent from a start
// button would write down a guess.
func TestDrivingAnUnknownTunnelRecordsNothing(t *testing.T) {
	d := testDesired(t)
	if err := d.SetRunning("de-1", "made-by-hand", true); err != nil {
		t.Fatalf("SetRunning: %v", err)
	}
	if len(d.Nodes()) != 0 {
		t.Fatalf("driving an unknown tunnel invented an intent: %v", d.Nodes())
	}
}
