package webui

// The restore points the updater keeps.
//
// This file used to hold the read-only access token as well — a second
// credential for a scraper or a peer panel. Nothing issued it and nothing used
// it, so it went; see requireReadAuth.

import (
	"net/http"

	"github.com/backpack/backpack/internal/manage"
)

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
