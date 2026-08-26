package webui

import (
	"net/http"

	"github.com/backpack/backpack/internal/manage"
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

	res, err := manage.RunSpeedTest(r.Context(), req.Name, req.Port)
	if err != nil {
		// The overwhelmingly likely cause is that nobody started the receiver
		// on the other server, and saying so is more use than the dial error.
		http.Error(w, err.Error()+" — check that the receiver is running on the "+
			"other server (sudo backpack → Manage → Speed Test → Receive)",
			http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{
		"mbps":    res.Mbps(),
		"bytes":   res.Bytes,
		"seconds": res.Duration.Seconds(),
		"summary": res.String(),
	})
}
