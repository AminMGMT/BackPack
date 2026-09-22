package webui

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/control"
	"github.com/backpack/backpack/internal/node"
)

// The drift endpoint, driven through the handler.
//
// control.Desired's own tests hold the comparison. What is left here is the
// half that only the panel can get wrong: asking every node it knows rather
// than only the ones it expects something of, telling a node that has drifted
// apart from one that could not be asked, and not turning a server that is down
// into a wall of differences.

func driftServer(t *testing.T) *server {
	t.Helper()
	s := newServer()
	s.want = control.NewDesiredAt(filepath.Join(t.TempDir(), "desired.json"))
	return s
}

func drift(t *testing.T, s *server) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleDrift(w, httptest.NewRequest("GET", "/api/fleet/drift", nil))
	if w.Code != 200 {
		t.Fatalf("drift returned %d: %s", w.Code, w.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, w.Body)
	}
	return out
}

// A node running what it was asked to run produces no differences and still
// appears, so the report can say it was checked.
func TestANodeThatMatchesIsReportedAsChecked(t *testing.T) {
	s := driftServer(t)
	f := newFake()
	f.up["de-1"] = true
	f.answers[node.OpList] = []node.TunnelState{{Name: "fr-relay", Active: true}}
	withFleet(s, f)

	if err := s.want.Record("de-1", control.TunnelIntent{Name: "fr-relay", Running: true}); err != nil {
		t.Fatalf("recording intent: %v", err)
	}

	out := drift(t, s)
	if total := out["total"].(float64); total != 0 {
		t.Fatalf("a matching node reported %v differences: %v", total, out)
	}
	nodes := out["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("expected one node in the report, got %d", len(nodes))
	}
	if reachable := nodes[0].(map[string]any)["reachable"].(bool); !reachable {
		t.Error("a node that answered was reported as unreachable")
	}
}

// The case the feature exists for.
func TestATunnelGoneFromANodeIsReported(t *testing.T) {
	s := driftServer(t)
	f := newFake()
	f.up["de-1"] = true
	f.answers[node.OpList] = []node.TunnelState{} // it has nothing
	withFleet(s, f)

	if err := s.want.Record("de-1", control.TunnelIntent{Name: "fr-relay", Running: true}); err != nil {
		t.Fatalf("recording intent: %v", err)
	}

	out := drift(t, s)
	if total := out["total"].(float64); total != 1 {
		t.Fatalf("a deleted tunnel produced %v differences: %v", total, out)
	}
	body, _ := json.Marshal(out)
	if !strings.Contains(string(body), string(control.DriftMissing)) {
		t.Fatalf("the report does not say the tunnel is missing: %s", body)
	}
}

// A server that is down has not drifted — it is not answering. Reporting it as
// drift would fill the report with the one thing the operator already knows.
func TestANodeThatCannotBeAskedIsNotReportedAsDrift(t *testing.T) {
	s := driftServer(t)
	f := newFake() // nothing is up
	withFleet(s, f)

	if err := s.want.Record("de-1", control.TunnelIntent{Name: "fr-relay", Running: true}); err != nil {
		t.Fatalf("recording intent: %v", err)
	}

	out := drift(t, s)
	if total := out["total"].(float64); total != 0 {
		t.Fatalf("an unreachable node produced %v differences", total)
	}
	n := out["nodes"].([]any)[0].(map[string]any)
	if n["reachable"].(bool) {
		t.Error("a node that did not answer was reported as reachable")
	}
	if n["error"] == nil || n["error"].(string) == "" {
		t.Error("the report does not say why the node could not be asked")
	}
}

// A node the panel expects nothing of is still asked, because a tunnel it is
// running that this panel did not create is worth naming and there is no intent
// to find it by.
func TestANodeWithNoIntentIsStillAsked(t *testing.T) {
	s := driftServer(t)
	f := newFake()
	f.up["de-1"] = true
	f.answers[node.OpList] = []node.TunnelState{{Name: "made-by-hand", Active: true}}
	withFleet(s, f)

	// Nothing recorded for de-1 at all — but it is in the fleet's own list, so
	// it has to be reached through the registry. The registry is on disk and
	// this test does not write there, so the node is reached through the
	// intent instead: record and then forget, which leaves the name known.
	if err := s.want.Record("de-1", control.TunnelIntent{Name: "placeholder"}); err != nil {
		t.Fatalf("recording: %v", err)
	}
	if err := s.want.Forget("de-1", "placeholder"); err != nil {
		t.Fatalf("forgetting: %v", err)
	}
	// Forgetting the last tunnel drops the node, which is correct — so record
	// one that is genuinely expected and check the unexpected one beside it.
	if err := s.want.Record("de-1", control.TunnelIntent{Name: "fr-relay", Running: true}); err != nil {
		t.Fatalf("recording: %v", err)
	}
	f.answers[node.OpList] = []node.TunnelState{
		{Name: "fr-relay", Active: true},
		{Name: "made-by-hand", Active: true},
	}

	out := drift(t, s)
	body, _ := json.Marshal(out)
	if !strings.Contains(string(body), string(control.DriftUnexpected)) {
		t.Fatalf("a tunnel the panel did not create was not named: %s", body)
	}
}

// With no runner at all the endpoint answers rather than failing: a panel whose
// fleet has not started is a panel that can still be asked what it expects.
func TestTheReportAnswersWithNoRunner(t *testing.T) {
	s := driftServer(t)
	if err := s.want.Record("de-1", control.TunnelIntent{Name: "fr-relay", Running: true}); err != nil {
		t.Fatalf("recording intent: %v", err)
	}
	out := drift(t, s)
	n := out["nodes"].([]any)[0].(map[string]any)
	if n["reachable"].(bool) {
		t.Error("a node was reported reachable with no runner")
	}
	if !strings.Contains(n["error"].(string), "runner") {
		t.Errorf("the report does not say the fleet is not started: %v", n["error"])
	}
}
