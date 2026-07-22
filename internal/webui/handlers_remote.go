package webui

import (
	"net/http"

	"github.com/backpack/backpack/internal/manage"
)

// handleRemoteToken manages this panel's own read-only token.
// GET returns it; POST action=generate mints a new one; POST action=revoke
// clears it, cutting off every scraper and peer that held it.
func (s *server) handleRemoteToken(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]string{"token": Load().RemoteToken})
	case http.MethodPost:
		r.ParseForm()
		c := Load()
		switch r.FormValue("action") {
		case "generate":
			c.RemoteToken = randomHex(24)
		case "revoke":
			c.RemoteToken = ""
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
		if err := Save(c); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"token": c.RemoteToken})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRestorePoints lists the snapshots the updater keeps. Read-only: a
// rollback replaces the running binary, which is a CLI decision (Update →
// Restore points), not a browser click.
func (s *server) handleRestorePoints(w http.ResponseWriter, r *http.Request) {
	snaps := manage.ListSnapshots()
	out := make([]map[string]any, len(snaps))
	for i, sn := range snaps {
		out[i] = map[string]any{
			"stamp":   sn.Meta.Stamp,
			"version": sn.Meta.Version,
			"created": sn.Meta.Created,
			"reason":  sn.Meta.Reason,
			"tunnels": sn.Meta.Tunnels,
		}
	}
	writeJSON(w, out)
}
