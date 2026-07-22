package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Drives the real handlers through an HTTP server, so the routes, methods and
// auth behave as they will in the browser.
func TestSettingsEndpointsRespond(t *testing.T) {
	srv := &server{sessions: newSessionStore()}
	tok := srv.sessions.create("127.0.0.1")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/telegram", srv.requireAuth(srv.handleTelegram))
	mux.HandleFunc("/api/relays", srv.requireAuth(srv.handleRelayOptions))
	mux.HandleFunc("/api/backup/export", srv.requireAuth(srv.handleBackupExport))

	ts := httptest.NewServer(mux)
	defer ts.Close()

	get := func(path string, authed bool) *http.Response {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		if authed {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	for _, p := range []string{"/api/telegram", "/api/relays"} {
		if r := get(p, false); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without a session = %d, want 401", p, r.StatusCode)
		}
		r := get(p, true)
		if r.StatusCode != http.StatusOK {
			t.Errorf("%s = %d, want 200", p, r.StatusCode)
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
			t.Errorf("%s content-type = %q, want JSON", p, ct)
		}
	}

	r := get("/api/backup/export", true)
	if r.StatusCode != http.StatusOK {
		t.Errorf("backup export = %d, want 200", r.StatusCode)
	}
	if cd := r.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("backup export should download as an attachment, got %q", cd)
	}
	if cc := r.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("a backup holds every token; Cache-Control = %q, want no-store", cc)
	}
}

func TestMaskTokenNeverLeaksTheSecret(t *testing.T) {
	const real = "123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"
	got := maskToken(real)

	if strings.Contains(got, "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw") {
		t.Errorf("the secret half is still present: %q", got)
	}
	if !strings.HasPrefix(got, "123456789:") {
		t.Errorf("the non-secret bot id should survive so the hint is useful: %q", got)
	}
	if maskToken("") != "" {
		t.Error("an unset token should mask to nothing at all")
	}
}
