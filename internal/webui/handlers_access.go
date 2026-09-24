package webui

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The panel's screens for access control: the tokens, and the record.

// handleTokens lists, issues and revokes API tokens.
//
// Guarded at ScopeAdmin: handing out a credential is not the same act as using
// one, and a write token must not be able to mint itself a better one.
func (s *server) handleTokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"tokens": tokenViews()})

	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch r.FormValue("action") {
		case "revoke":
			if err := RevokeToken(strings.TrimSpace(r.FormValue("name"))); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"tokens": tokenViews()})

		default: // issue
			scope, err := ParseScope(r.FormValue("scope"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			days, _ := strconv.Atoi(r.FormValue("days"))
			if days <= 0 {
				days = 90
			}
			if days > 3650 {
				http.Error(w, "an expiry that far out is the same as none at all", http.StatusBadRequest)
				return
			}
			secret, tok, err := IssueToken(r.FormValue("name"), scope,
				time.Duration(days)*24*time.Hour)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			// The one and only time the secret exists in readable form. The
			// panel says so on the screen, because an operator who closes the
			// dialog without copying it has to issue a new one.
			writeJSON(w, map[string]any{
				"secret": secret,
				"token":  view(tok),
				"tokens": tokenViews(),
			})
		}

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAudit returns the record of what has been done through the panel.
func (s *server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 200
	}
	entries := Audit(limit)
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, describeAudit(e))
	}
	intact, brokenAt, head := AuditIntegrity()
	writeJSON(w, map[string]any{"entries": entries, "lines": lines,
		"intact": intact, "brokenAt": brokenAt, "head": shortHash(head)})
}

// tokenView is a token as the panel shows it — never the secret, which only
// exists in the response that created it.
type tokenView struct {
	Name     string `json:"name"`
	Scope    string `json:"scope"`
	Created  int64  `json:"created"`
	Expires  int64  `json:"expires"`
	LastUsed int64  `json:"lastUsed,omitempty"`
	Expired  bool   `json:"expired"`
	// Unused says nothing has ever presented this token. It is the field that
	// makes an old list prunable: a token nothing uses is one that can go.
	Unused bool `json:"unused"`
}

func view(t APIToken) tokenView {
	return tokenView{
		Name: t.Name, Scope: t.Scope.String(),
		Created: t.Created, Expires: t.Expires, LastUsed: t.LastUsed,
		Expired: t.Expired(time.Now()), Unused: t.LastUsed == 0,
	}
}

func tokenViews() []tokenView {
	list := ListTokens()
	out := make([]tokenView, 0, len(list))
	for _, t := range list {
		out = append(out, view(t))
	}
	return out
}
