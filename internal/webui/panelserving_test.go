package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// There is one panel, and "/" is where it is.
//
// It was the second of two for as long as it was being rebuilt: the classic
// dashboard answered "/", this one answered /panel/, and a per-server setting
// decided which of them a browser landed on. Everything about that arrangement
// existed to make an unfinished panel safe to offer, and all of it — the
// setting, the /api/panelui endpoint, the ?panel=classic escape hatch, the
// single-file dashboard itself — is gone.
//
// What must not come back is a second thing served at "/", because the reason
// there were two was never a good one to have twice.
func TestThePanelIsWhatIsServedAtTheRoot(t *testing.T) {
	srv := &server{sessions: newSessionStore()}

	w := httptest.NewRecorder()
	srv.handlePanel(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `src="js/main.js"`) {
		t.Error("the panel shell was not what was served at /")
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content-type = %q, want html", ct)
	}
}

// The assets it asks for resolve from the root, because index.html names them
// relatively and the page is now reached at "/" rather than at a subdirectory.
func TestThePanelAssetsResolveFromTheRoot(t *testing.T) {
	srv := &server{sessions: newSessionStore()}

	for _, asset := range []string{"/css/tokens.css", "/js/main.js", "/js/api.js", "/views/settings.html"} {
		w := httptest.NewRecorder()
		srv.handlePanel(w, httptest.NewRequest("GET", asset, nil))
		if w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", asset, w.Code)
		}
	}
}

// A path that names nothing still 404s. The panel handler is registered at "/",
// so it is also the catch-all, and a catch-all that answered everything with
// the shell would turn every typo into a page.
func TestAnUnknownPathIsStillNotFound(t *testing.T) {
	srv := &server{sessions: newSessionStore()}
	w := httptest.NewRecorder()
	srv.handlePanel(w, httptest.NewRequest("GET", "/nothing-is-here", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /nothing-is-here = %d, want 404", w.Code)
	}
}

// /panel/ is where the panel answered for as long as there were two, which is
// long enough for the address to have been bookmarked, pinned or sent to
// somebody. It redirects rather than breaking.
func TestTheOldPanelPathStillLeadsSomewhere(t *testing.T) {
	srv := &server{sessions: newSessionStore()}

	w := httptest.NewRecorder()
	srv.handleOldPanelPath(w, httptest.NewRequest("GET", panelPrefix, nil))
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("GET %s = %d, want a permanent redirect", panelPrefix, w.Code)
	}
	if got := w.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}

	// A deep link keeps its path: /panel/css/tokens.css is a real asset that
	// somebody's cached page may still ask for.
	w = httptest.NewRecorder()
	srv.handleOldPanelPath(w, httptest.NewRequest("GET", panelPrefix+"css/tokens.css", nil))
	if got := w.Header().Get("Location"); got != "/css/tokens.css" {
		t.Errorf("Location = %q, want /css/tokens.css", got)
	}
}

// The panel must not carry a second source of truth.
func TestThePanelHasNoSecondSourceOfData(t *testing.T) {
	loadPanel()

	api, err := fs.ReadFile(panelRoot, "js/api.js")
	if err != nil {
		t.Fatalf("the panel's api.js is not embedded: %v", err)
	}
	for _, gone := range []string{"__BACKPACK_LIVE__", "MOCK", "mock/"} {
		if strings.Contains(string(api), gone) {
			t.Errorf("api.js has grown a second source of data again (%q)", gone)
		}
	}
	if strings.Contains(string(panelIndex), "__BACKPACK_LIVE__") {
		t.Error("the served page still injects the mock-mode flag")
	}
	if _, err := fs.Stat(panelRoot, "mock"); err == nil {
		t.Error("panel/mock is back")
	}
}

// The fixtures must not ship. A binary carrying mock/*.json has a second,
// invented source of truth inside it, one ?mock=1 away from being believed.
func TestTheFixturesAreNotInTheBinary(t *testing.T) {
	loadPanel()

	if _, err := fs.ReadFile(panelRoot, "mock/stats.json"); err == nil {
		t.Error("the preview's fixture data is embedded in the binary")
	}
	if _, err := fs.ReadFile(panelRoot, "serve.py"); err == nil {
		t.Error("the preview's dev server is embedded in the binary")
	}

	srv := &server{sessions: newSessionStore()}
	w := httptest.NewRecorder()
	srv.handlePanel(w, httptest.NewRequest("GET", "/mock/stats.json", nil))
	if w.Code == http.StatusOK {
		t.Error("the server answered a request for fixture data")
	}
}

// Nothing may offer a way back to a panel that no longer exists.
func TestNothingOffersThePanelThatIsGone(t *testing.T) {
	loadPanel()

	settings, err := fs.ReadFile(panelRoot, "views/settings.html")
	if err != nil {
		t.Fatalf("settings view: %v", err)
	}
	for _, gone := range []string{"panel=classic", "Classic panel", "/api/panelui"} {
		if strings.Contains(string(settings), gone) {
			t.Errorf("the settings view still offers %q, which nothing serves any more", gone)
		}
	}
}
