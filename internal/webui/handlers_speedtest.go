package webui

import (
	"net/http"

	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/node"
)

// The Speed Test, from the panel.
//
// Two requests, because the measurement is not something to start by accident.
// The first asks what a measurement would involve — which mapping it would run
// through, and therefore which service on the far server goes quiet while it
// does. The page shows that and waits. The second runs it.
//
// Both are write-authenticated. Reading a tunnel's plan says which ports its
// backends are on, and the measurement itself pushes real traffic and takes a
// real service down for ten seconds; neither belongs to the read-only token.

// handleSpeedTestPlan answers what this machine can measure for one tunnel.
func (s *server) handleSpeedTestPlan(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	plan, err := manage.SpeedTestPlanFor(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, plan)
}

// handleSpeedTestRun measures and reports.
//
// It holds the request open for the length of the measurement — about ten
// seconds — which is why RunSpeedTest bounds itself well inside the server's
// write timeout. A request that outlives the response is a measurement nobody
// gets to see.
func (s *server) handleSpeedTestRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name string `json:"name"`
		Port int    `json:"port"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}

	// The measurement needs something at the far end to sink the bytes, and
	// until now that was a person: the error below told the operator to go and
	// start a receiver from a CLI menu on the other server. When that server is
	// managed from here, this asks it to do that itself — which is the same
	// second pass the whole node feature exists to remove.
	//
	// Best-effort on purpose. A receiver that will not start is reported by the
	// measurement failing, with the wording it already had, and a tunnel whose
	// far end is not a managed server behaves exactly as it did.
	started, recvErr := "", ""
	if nodeName, paired := manage.NodeFor(req.Name); paired {
		if hub := s.nodes.get(); hub != nil && hub.IsOnline(nodeName) {
			port := req.Port
			if plan, perr := manage.SpeedTestPlanFor(req.Name); perr == nil {
				for _, t := range plan.Targets {
					if t.ListenPort == req.Port {
						port = t.BackendPort
					}
				}
				if plan.Kind != "forward" && plan.Port > 0 {
					port = plan.Port
				}
			}
			if err := hub.Call(nodeName, node.OpReceive,
				node.ReceiveRequest{Port: port, Seconds: 40}, nil); err == nil {
				started = nodeName
			} else {
				// Almost always: the real backend still holds that port. The
				// measurement can still run — the bytes cross the tunnel and
				// the service on the far side reads them — but what comes back
				// is then partly that service's appetite rather than the
				// tunnel's capacity. Reported rather than swallowed, because a
				// number nobody can qualify is worse than one with a caveat.
				recvErr = err.Error()
			}
		}
	}

	res, err := manage.RunSpeedTest(r.Context(), req.Name, req.Port)
	if err != nil {
		// The overwhelmingly likely cause is that nobody started the receiver
		// on the other server, and saying so is more use than the dial error.
		http.Error(w, err.Error()+" — check that the receiver is running on the "+
			"other server (sudo backpack → Manage → Speed Test → Receive)",
			http.StatusBadGateway)
		return
	}
	out := map[string]any{
		"mbps":    res.Mbps(),
		"bytes":   res.Bytes,
		"seconds": res.Duration.Seconds(),
		"summary": res.String(),
	}
	if started != "" {
		out["receiver"] = started
	}
	if recvErr != "" {
		out["receiverError"] = recvErr
	}
	writeJSON(w, out)
}
