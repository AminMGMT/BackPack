package webui

import (
	"net/http"

	"github.com/backpack/backpack/internal/control"
	"github.com/backpack/backpack/internal/node"
)

// What the fleet is supposed to be running, against what it is.
//
// Every fleet operation was imperative: the panel told a node to create a
// tunnel, the node said it had, and that was the end of it. Nothing remembered
// the instruction, so nothing could notice it had stopped being true — a tunnel
// removed on the far machine, a unit disabled during an incident and never
// re-enabled, an apply that reported success and then lost its config. All of
// them leave a fleet that looks correct here and is not.
//
// This asks each node what it has and compares it with what was written down.
// It changes nothing: see internal/control/desired.go for why re-applying on
// its own would be the wrong shape for this product in particular.

// DriftReport is one node's answer.
type DriftReport struct {
	Node string `json:"node"`
	// Reachable is false when the node could not be asked. It is not drift —
	// a server that is down has not changed, it is simply not answering — and
	// reporting it as drift would fill the report with the one thing an
	// operator already knows.
	Reachable bool            `json:"reachable"`
	Error     string          `json:"error,omitempty"`
	Expected  int             `json:"expected"`
	Drift     []control.Drift `json:"drift,omitempty"`
}

// handleDrift compares every node in the fleet with what it is expected to run.
func (s *server) handleDrift(w http.ResponseWriter, r *http.Request) {
	run := s.nodes.Runner()

	// Every node the panel knows, not only the ones something is expected of:
	// a node running tunnels this panel never created is worth reporting, and
	// it has no intent to find it by.
	names := map[string]bool{}
	for _, n := range node.List() {
		names[n.Name] = true
	}
	for _, n := range s.want.Nodes() {
		names[n] = true
	}

	reports := make([]DriftReport, 0, len(names))
	for name := range names {
		rep := DriftReport{Node: name, Expected: len(s.want.Intent(name))}
		if run == nil {
			rep.Error = "the fleet runner is not started"
			reports = append(reports, rep)
			continue
		}
		var states []node.TunnelState
		if err := run.Call(name, node.OpList, nil, &states); err != nil {
			rep.Error = err.Error()
			reports = append(reports, rep)
			continue
		}
		rep.Reachable = true
		rep.Drift = s.want.Reconcile(name, states)
		reports = append(reports, rep)
	}

	// A stable order, so two readings of an unchanged fleet are the same
	// document and a difference between them means something.
	sortDriftReports(reports)

	total := 0
	for _, rep := range reports {
		total += len(rep.Drift)
	}
	writeJSON(w, map[string]any{"nodes": reports, "total": total})
}

func sortDriftReports(reports []DriftReport) {
	for i := 1; i < len(reports); i++ {
		for j := i; j > 0 && reports[j].Node < reports[j-1].Node; j-- {
			reports[j], reports[j-1] = reports[j-1], reports[j]
		}
	}
}
