package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A request that changes something has to have come from this panel.
//
// What stopped a cross-site request was a chain of three: a SameSite=Lax
// session cookie, a POST requirement on every mutating handler, and a base path
// nobody can guess. Each link holds, and the shape of it is what is worth
// noticing — the last link is a secret in the address bar, which makes the base
// path a load-bearing control rather than the obscurity it is described as.
//
// Sec-Fetch-Site is the one check that is about the question being asked, and a
// browser will not lie about it.
func TestACrossSiteWriteIsRefused(t *testing.T) {
	reached := false
	h := withPanelSecurity(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))

	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		reached = false
		r := httptest.NewRequest(method, "/api/tunnel/create", strings.NewReader("{}"))
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if reached {
			t.Errorf("a cross-site %s reached the handler", method)
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("a cross-site %s answered %d, want 403", method, w.Code)
		}
	}
}

// The panel's own requests are unaffected, and so is anything that is not a
// browser — a script, a peer panel holding the remote access token, curl.
func TestThePanelsOwnWritesAndNonBrowserCallersGetThrough(t *testing.T) {
	for _, site := range []string{"same-origin", "same-site", "none", ""} {
		reached := false
		h := withPanelSecurity(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			reached = true
		}))
		r := httptest.NewRequest("POST", "/api/tunnel/create", strings.NewReader("{}"))
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		if !reached {
			t.Errorf("a POST with Sec-Fetch-Site %q was refused", site)
		}
	}
}

// Reading is not refused whoever asks: the read-only endpoints are meant to be
// reachable by a scraper, and a cross-site GET cannot change anything.
func TestACrossSiteReadIsStillAnswered(t *testing.T) {
	reached := false
	h := withPanelSecurity(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	r := httptest.NewRequest("GET", "/api/stats", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !reached {
		t.Error("a cross-site GET was refused")
	}
}

// Signing out is a GET, and the one that has to stay a link — so it is checked
// where it is handled rather than by the method rule above.
func TestACrossSiteLogoutDoesNotSignTheOperatorOut(t *testing.T) {
	srv := &server{sessions: newSessionStore(), nodes: &fleet{}}
	r := httptest.NewRequest("GET", "/logout", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	srv.handleLogout(w, r)

	if loc := w.Header().Get("Location"); strings.Contains(loc, "/login") {
		t.Error("a page on another site signed the operator out by navigating to /logout")
	}
}
