package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A POST from a sibling site on the same domain is another origin, and the
// session cookie is sent on it: it is refused like a cross-site one. The
// panel's own pages, typed addresses and non-browser callers are not.
func TestAChangeFromASiblingSiteIsRefused(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := withPanelSecurity(ok)
	for site, want := range map[string]int{
		"same-site":   http.StatusForbidden,
		"cross-site":  http.StatusForbidden,
		"same-origin": http.StatusNoContent,
		"none":        http.StatusNoContent,
		"":            http.StatusNoContent,
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/tunnel/action", nil)
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Sec-Fetch-Site %q: status %d, want %d", site, w.Code, want)
		}
	}
}
