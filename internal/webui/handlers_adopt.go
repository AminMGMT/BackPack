package webui

import (
	"net/http"
	"strings"

	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/node"
)

// Linking a tunnel that already exists to the server that holds its other end.
//
// Everything the fleet does for a tunnel — carrying an edit across, starting
// both halves together, reading the far journal, standing the speed test's
// receiver up on the other machine — is gated on a record saying where that
// other half lives. Until now that record was written in exactly one place: the
// moment the panel created both ends at once. A tunnel made any other way, or
// made before the server was added to the fleet, could never get one.
//
// So a fleet could hold the very server a tunnel runs on and do none of it.

// thisServerHosts is what a client on the far side would have been aimed at.
func thisServerHosts() []string {
	var out []string
	for _, h := range []string{manage.PublicIPv4(), manage.PublicIPv6()} {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// farEndsOn asks one managed server what tunnels it has.
func farEndsOn(run node.Runner, server string) ([]node.TunnelState, error) {
	var states []node.TunnelState
	if err := run.Call(server, node.OpList, nil, &states); err != nil {
		return nil, err
	}
	return states, nil
}

// candidatesFor ranks a server's tunnels against one local tunnel.
func candidatesFor(local manage.Tunnel, states []node.TunnelState) []manage.PairCandidate {
	far := make([]manage.FarEnd, 0, len(states))
	for _, s := range states {
		far = append(far, manage.FarEnd{
			Name: s.Name, Role: s.Role, TunnelPort: s.TunnelPort, ServerHost: s.ServerHost,
		})
	}
	return manage.MatchFarEndsFor(local.Name, far, thisServerHosts())
}

// handleTunnelAdopt lists what a server could hold for a tunnel (GET) and
// records the choice (POST).
//
// The two are separate acts on purpose. What the panel can show is what could
// be the other end; which one it is, is the operator's to confirm — a pairing
// written on a guess sends the next edit to a tunnel somebody else is using.
func (s *server) handleTunnelAdopt(w http.ResponseWriter, r *http.Request) {
	run := s.nodes.get()
	if run == nil {
		http.Error(w, "no fleet on this panel", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		server := strings.TrimSpace(r.URL.Query().Get("node"))
		local, ok := manage.Find(name)
		if !ok {
			http.Error(w, "no such tunnel", http.StatusNotFound)
			return
		}
		if server == "" {
			http.Error(w, "name the server to look on", http.StatusBadRequest)
			return
		}
		states, err := farEndsOn(run, server)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// Everything it has, with the ones that could be this tunnel marked.
		// The whole list is offered because a match this cannot demonstrate is
		// still one the operator may know to be right.
		cand := candidatesFor(local, states)
		mark := map[string]manage.PairCandidate{}
		for _, c := range cand {
			mark[c.Name] = c
		}
		rows := make([]map[string]any, 0, len(states))
		for _, st := range states {
			row := map[string]any{"name": st.Name, "role": st.Role, "active": st.Active}
			if c, ok := mark[st.Name]; ok {
				row["certain"], row["why"] = c.Certain, c.Why
			}
			rows = append(rows, row)
		}
		writeJSON(w, map[string]any{"node": server, "tunnels": rows})

	case http.MethodPost:
		r.ParseForm()
		name := strings.TrimSpace(r.FormValue("name"))
		server := strings.TrimSpace(r.FormValue("node"))
		peer := strings.TrimSpace(r.FormValue("peerName"))
		if name == "" || server == "" || peer == "" {
			http.Error(w, "name the tunnel, the server and the tunnel on it", http.StatusBadRequest)
			return
		}
		if _, ok := manage.Find(name); !ok {
			http.Error(w, "no such tunnel", http.StatusNotFound)
			return
		}
		// Confirmed against the server rather than taken on trust: a pairing
		// pointing at a tunnel that is not there fails later, during an edit,
		// where it looks like the edit is broken.
		states, err := farEndsOn(run, server)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		found := false
		for _, st := range states {
			if strings.EqualFold(st.Name, peer) {
				found = true
				peer = st.Name
			}
		}
		if !found {
			http.Error(w, server+" has no tunnel called "+peer, http.StatusBadRequest)
			return
		}
		if err := manage.NoteNodePair(name, server, peer); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		farService.forget(server)
		writeJSON(w, map[string]any{"status": "ok", "name": name, "node": server, "peerName": peer})

	case http.MethodDelete:
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if err := manage.ForgetNodePair(name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"status": "ok"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// suggestPairsOn is the same matching, run for every unpaired tunnel when a
// server joins the fleet.
//
// It suggests and never links. The match is strong — two ends that meet at an
// address — but "strong" is not "the operator's decision", and a pairing made
// without one is a decision about where their next edit goes.
func (s *server) suggestPairsOn(run node.Runner, server string) []map[string]any {
	if run == nil {
		return nil
	}
	states, err := farEndsOn(run, server)
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, t := range manage.List() {
		if _, paired := manage.NodeFor(t.Name); paired {
			continue
		}
		for _, c := range candidatesFor(t, states) {
			if !c.Certain {
				continue // only demonstrated matches are worth interrupting for
			}
			out = append(out, map[string]any{
				"name": t.Name, "node": server, "peerName": c.Name, "why": c.Why,
			})
			break
		}
	}
	return out
}
