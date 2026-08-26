package webui

import (
	"net/http"
	"time"

	"github.com/backpack/backpack/internal/manage"
)

// A tunnel's configuration history, from the panel.
//
// The store keeps the superseded configurations themselves, and this never
// sends them. A config holds the tunnel's token, and a list of them delivered to
// a browser is that token in a page, in a cache, and in whatever the browser
// does with either. What the panel needs is the moments — enough to say "put
// back the one from before this" — and the moments carry nothing secret.

// confChange is one entry as the panel sees it: when, and what was being done.
type confChange struct {
	At   int64  `json:"at"`
	When string `json:"when"`
	Note string `json:"note,omitempty"`
}

// handleConfHistory lists the moments a tunnel's configuration changed.
func (s *server) handleConfHistory(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	hist := manage.ConfigHistory(name)
	out := make([]confChange, 0, len(hist))
	for _, c := range hist {
		out = append(out, confChange{
			At:   c.At.UnixNano(),
			When: c.At.Format("2 Jan 15:04"),
			Note: c.Note,
		})
	}
	writeJSON(w, map[string]any{"changes": out})
}

// handleConfRestore puts back the configuration from before one of them.
//
// The moment is carried as nanoseconds because that is what identifies an entry:
// two edits a second apart are two entries, and a second-resolution timestamp
// would restore whichever the list happened to reach first.
func (s *server) handleConfRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name string `json:"name"`
		At   int64  `json:"at"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.At == 0 {
		http.Error(w, "missing name or moment", http.StatusBadRequest)
		return
	}

	// RestoreConfigFrom reverts the tunnel if the restored configuration will
	// not come up, so a failure here has already left the tunnel as it was.
	if err := manage.RestoreConfigFrom(req.Name, time.Unix(0, req.At)); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
