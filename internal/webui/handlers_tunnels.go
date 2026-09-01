package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/backpack/backpack/internal/manage"
)

// Tunnel management endpoints.
//
// The panel used to be able only to watch: tunnels were created, edited and
// restarted from the CLI menu over SSH, and the dashboard said "monitoring
// only" out loud. Everything here is that menu, reachable from the browser —
// the setup wizard, the edit screen and the four service actions.
//
// Nothing new is decided in this file. Each handler validates that the request
// is well-formed and hands it to the same manage function the CLI calls, so
// there is one definition of what a tunnel is and one place where it is
// written. All of it sits behind requireAuth: the read-only remote token can
// watch a tunnel, never change one.

// maxTunnelBody caps a setup or edit request. A filled form is a couple of
// kilobytes; anything near this is a mistake or an attack.
const maxTunnelBody = 64 << 10

// decodeJSON reads a JSON request body into v, with a size cap.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxTunnelBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// handleTunnelOptions serves the menus the setup form is built from: the
// transport families with their variants, the performance presets, and the
// menus the advanced drawers need — the spoof carrier's packet profiles, the
// packet carrier's flag cycles and this machine's network interfaces. They come
// from manage rather than being written into the page, so a transport added to
// the CLI appears in the panel without touching the HTML.
func (s *server) handleTunnelOptions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"families":      manage.TransportFamilies(),
		"presets":       manage.Presets(),
		"spoofProfiles": manage.SpoofProfiles(),
		"pckFlags":      manage.PckFlagCycles(),
		"interfaces":    manage.RoutableInterfaces(),
	})
}

// handleTunnelSuggest answers the two "roll one for me" buttons: a fresh
// 64-character token, and a free four-digit port.
func (s *server) handleTunnelSuggest(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("what") {
	case "token":
		writeJSON(w, map[string]any{"token": manage.NewToken()})
	case "port":
		p := manage.SuggestPort()
		if p == 0 {
			http.Error(w, "could not find a free port to suggest", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, map[string]any{"port": p})
	default:
		http.Error(w, "ask for token or port", http.StatusBadRequest)
	}
}

// handleTunnelDefaults returns the advanced settings a preset produces for a
// given role and transport — what the Fine Tune drawer shows before anything is
// edited, and what it snaps back to when the preset changes.
func (s *server) handleTunnelDefaults(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	writeJSON(w, manage.PresetTune(q.Get("preset"), q.Get("role"), q.Get("transport")))
}

// handleTunnelCreate builds a tunnel from the setup form.
//
// A tunnel that is created but does not come up is reported as such rather than
// as a failure: the config is on disk either way, and the usual cause — a port
// already in use — is something the operator fixes by editing the tunnel, not
// by creating it again.
func (s *server) handleTunnelCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var n manage.NewTunnel
	if err := decodeJSON(w, r, &n); err != nil {
		http.Error(w, "could not read the form: "+err.Error(), http.StatusBadRequest)
		return
	}
	service, active, err := manage.CreateTunnel(n)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{
		"status":  "ok",
		"name":    strings.TrimSpace(n.Name),
		"service": service,
		"active":  active,
	})
}

// handleTunnelSettings serves one tunnel's editable settings, for filling the
// Edit form.
func (s *server) handleTunnelSettings(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	// A direct tunnel keeps its settings in its own table, and TunnelSettingsOf
	// reads [server] and [client] — so sending one there came back as "not a
	// client tunnel" and the Edit button did nothing at all.
	if t, ok := manage.Find(name); ok && manage.IsDirectKind(t) {
		set, err := manage.DirectSettingsOf(name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"kind": "direct", "direct": set})
		return
	}

	set, err := manage.TunnelSettingsOf(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, set)
}

// tunnelEditRequest is the Edit form: which tunnel, and what to change.
type tunnelEditRequest struct {
	Name string `json:"name"`
	manage.TunnelEdit

	// Direct carries the direct form's own fields. Separate from the embedded
	// reverse edit rather than merged into it, because the two share almost no
	// keys and a merged struct would let a reverse form silently set a
	// layer-3-only value.
	Direct manage.DirectEdit `json:"direct"`
}

// handleTunnelEdit applies the Edit form. Every change lands in one write and
// one restart, and a tunnel that will not come up on the new settings is put
// back on the old ones — the same guarantee the CLI's edit screen gives.
func (s *server) handleTunnelEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tunnelEditRequest
	if err := decodeJSON(w, r, &req); err != nil {
		http.Error(w, "could not read the form: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	if t, ok := manage.Find(req.Name); ok && manage.IsDirectKind(t) {
		if err := manage.EditDirectSettings(req.Name, req.Direct); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
		return
	}
	if err := manage.EditTunnelSettings(req.Name, req.TunnelEdit); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleTunnelAction runs one of the four service actions on a tunnel, or
// restarts every tunnel at once.
func (s *server) handleTunnelAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.ParseForm()
	action := r.FormValue("action")

	// Restarting everything names no tunnel, so it is answered before the name
	// is required.
	if action == "restartall" {
		ok, failed := manage.RestartAll()
		writeJSON(w, map[string]any{"status": "ok", "restarted": ok, "failed": failed})
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	if _, ok := manage.Find(name); !ok {
		http.Error(w, fmt.Sprintf("no such tunnel %q", name), http.StatusNotFound)
		return
	}

	var err error
	switch action {
	case "start":
		err = manage.Start(name)
	case "stop":
		err = manage.Stop(name)
	case "restart":
		err = manage.Restart(name)
	case "delete":
		err = manage.Delete(name)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// The direct-tunnel half of the panel's setup form.
//
// Separate handlers from the reverse ones, on the same terms as everything else
// in the direct work: the reverse create path is in production, and there is no
// version of "add a branch to it" that cannot break it.

// handleDirectOptions serves the lists the direct form is built from — the
// carriers and the tuning presets — so the panel and the CLI wizard cannot
// drift into offering different things.
func (s *server) handleDirectOptions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"carriers": manage.DirectCarriers(),
		"presets":  manage.DirectPresets(),
		// The forged-source carrier's packet profiles, so the form offers the
		// same list the CLI does rather than a copy that drifts.
		"spoofProfiles": manage.SpoofProfiles(),
	})
}

// handleDirectDefaults fills a blank direct form: a free subnet, a free
// interface, a suggested port, and a token.
//
// The token is offered to the kharej side only. Offering one on both ends means
// somebody accepts the default twice and ends up with two different tokens —
// and a mismatched token is answered with silence by design, so it presents as
// a blocked port rather than as the typo it is. The CLI wizard is asymmetric
// for the same reason; the panel has to be too, or the two disagree about the
// one value that must match.
func (s *server) handleDirectDefaults(w http.ResponseWriter, r *http.Request) {
	side := r.URL.Query().Get("side")
	out := manage.SuggestDirectDefaults(side)
	out["tunnelPort"] = manage.SuggestDirectPort()
	if !strings.EqualFold(strings.TrimSpace(side), "kharej") {
		delete(out, "token")
	}
	writeJSON(w, out)
}

// handleDirectCreate builds a direct tunnel from the form.
func (s *server) handleDirectCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var n manage.NewDirectTunnel
	if err := decodeJSON(w, r, &n); err != nil {
		http.Error(w, "could not read the form: "+err.Error(), http.StatusBadRequest)
		return
	}
	service, active, err := manage.CreateDirectTunnel(n)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{
		"status":  "ok",
		"name":    strings.TrimSpace(n.Name),
		"service": service,
		"active":  active,
	})
}
