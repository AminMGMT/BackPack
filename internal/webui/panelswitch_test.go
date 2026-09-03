package webui

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The switch between the finished panel and the one being built beside it.
//
// What these hold down is the part that is easy to get wrong quietly: a new
// panel that silently shows fixture data on a real server, and an operator who
// turns it on and cannot get back.

// The default must be the finished panel. An upgrade that moved every existing
// install onto an unfinished UI would be the worst possible way to ship it.
func TestAnUntouchedServerStillGetsTheClassicPanel(t *testing.T) {
	if Load().ExperimentalPanel {
		t.Fatal("a server with no configuration reports the experimental panel as chosen")
	}

	srv := &server{sessions: newSessionStore()}
	w := httptest.NewRecorder()
	srv.handleDashboard(w, httptest.NewRequest("GET", "/", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `id="accb-interface"`) {
		t.Error("GET / did not serve the classic dashboard")
	}
}

// The escape hatch. An operator whose new panel will not paint has to be able
// to type one address and be back — no JavaScript on the broken page, no API
// call, no session juggling. Both directions answer, so the same address bar
// gets them in as well as out.
func TestThePanelChoiceIsReachableFromTheAddressBar(t *testing.T) {
	srv := &server{sessions: newSessionStore()}

	for _, tc := range []struct{ query, want string }{
		{"classic", "/"},
		{"next", panelPrefix},
	} {
		w := httptest.NewRecorder()
		srv.handleDashboard(w, httptest.NewRequest("GET", "/?panel="+tc.query, nil))

		if w.Code != http.StatusSeeOther {
			t.Errorf("?panel=%s = %d, want 303", tc.query, w.Code)
		}
		if got := w.Header().Get("Location"); got != tc.want {
			t.Errorf("?panel=%s went to %q, want %q", tc.query, got, tc.want)
		}
	}
}

// Anything that is not one of the two choices means the classic panel, because
// there is no third thing to fall back to and stranding somebody on a typo is
// the one outcome worth ruling out.
func TestAnUnknownPanelChoiceLandsOnTheClassicOne(t *testing.T) {
	srv := &server{sessions: newSessionStore()}
	w := httptest.NewRecorder()
	srv.handleDashboard(w, httptest.NewRequest("GET", "/?panel=nonsense", nil))

	if got := w.Header().Get("Location"); got != "/" {
		t.Errorf("an unrecognised choice went to %q, want /", got)
	}
}

// There is one code path and it is the real one.
//
// The panel used to carry a second: fixtures under mock/, chosen by a flag the
// Go handler injected. It was how the screens were drawn before the server
// existed, and it outlived that twice over — the fixtures were never shipped
// inside the binary, so `?mock=1` on a real panel fetched files that are not
// there and drew an empty server over a busy one; and having somewhere for
// invented numbers to come from is what let so many of them survive on screens
// that were supposed to have been wired up. This keeps it gone.
func TestThePanelHasNoSecondSourceOfData(t *testing.T) {
	loadExperimentalPanel()

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
	// And the fixtures themselves are not in the tree any more.
	if _, err := fs.Stat(panelRoot, "mock"); err == nil {
		t.Error("panel/mock is back")
	}
}

// What is served at /panel/ is the panel itself, and the assets it asks for
// resolve — index.html names them relatively, so the trailing slash on the
// prefix is load-bearing.
func TestTheNewPanelServesItselfAndItsAssets(t *testing.T) {
	srv := &server{sessions: newSessionStore()}

	w := httptest.NewRecorder()
	srv.handleExperimentalPanel(w, httptest.NewRequest("GET", panelPrefix, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", panelPrefix, w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `src="js/main.js"`) {
		t.Error("the panel shell was not what was served")
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content-type = %q, want html", ct)
	}

	for _, asset := range []string{"css/tokens.css", "js/main.js", "js/api.js"} {
		w := httptest.NewRecorder()
		srv.handleExperimentalPanel(w, httptest.NewRequest("GET", panelPrefix+asset, nil))
		if w.Code != http.StatusOK {
			t.Errorf("GET %s%s = %d, want 200", panelPrefix, asset, w.Code)
		}
	}
}

// The fixtures must not ship. A binary carrying mock/*.json has a second,
// invented source of truth inside it, one ?mock=1 away from being believed.
func TestTheFixturesAreNotInTheBinary(t *testing.T) {
	loadExperimentalPanel()

	if _, err := fs.ReadFile(panelRoot, "mock/stats.json"); err == nil {
		t.Error("the preview's fixture data is embedded in the binary")
	}
	if _, err := fs.ReadFile(panelRoot, "serve.py"); err == nil {
		t.Error("the preview's dev server is embedded in the binary")
	}

	srv := &server{sessions: newSessionStore()}
	w := httptest.NewRecorder()
	srv.handleExperimentalPanel(w, httptest.NewRequest("GET", panelPrefix+"mock/stats.json", nil))
	if w.Code == http.StatusOK {
		t.Error("the server answered a request for fixture data")
	}
}

// Reading the setting must work on a server that has never written one, which
// is every server until somebody switches.
func TestThePanelChoiceEndpointReportsTheDefault(t *testing.T) {
	srv := &server{sessions: newSessionStore()}
	w := httptest.NewRecorder()
	srv.handlePanelUI(w, httptest.NewRequest("GET", "/api/panelui", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200", w.Code)
	}
	var got struct {
		Experimental bool `json:"experimental"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if got.Experimental {
		t.Error("an unconfigured server reports the experimental panel as chosen")
	}

	w = httptest.NewRecorder()
	srv.handlePanelUI(w, httptest.NewRequest("PUT", "/api/panelui", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT = %d, want 405", w.Code)
	}
}

// Both panels have to offer the switch, or one of them is a trap: Settings is
// the way in, and the menu item is the way back for somebody who is already
// there and does not know the address.
func TestBothPanelsOfferTheSwitch(t *testing.T) {
	classic := string(dashboardHTML)
	for _, want := range []string{`id="expui"`, "/api/panelui", "?panel=classic"} {
		if !strings.Contains(classic, want) {
			t.Errorf("the classic panel's settings do not mention %q", want)
		}
	}
	if !strings.Contains(classic, "loadPanelUI();") {
		t.Error("the switch is never read back, so it shows the wrong state when Settings opens")
	}
	// And in its menu, where the other panel keeps the same choice. It used to
	// live only as a checkbox two clicks deep in Settings → Interface, so the
	// two panels were not equally easy to leave.
	if !strings.Contains(classic, "/?panel=next") {
		t.Error("the classic panel's menu offers no one-click way over to the new one")
	}

	// The new panel's own way back. It used to be in the header menu; that menu
	// is gone — everything in it had another door — so the switch moved to
	// Settings → Panel access, where which panel this browser gets belongs.
	// What matters is that it exists somewhere the panel actually serves, not
	// which file it is in.
	loadExperimentalPanel()
	found := false
	err := fs.WalkDir(panelRoot, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(p, ".js") && !strings.HasSuffix(p, ".html") {
			return nil
		}
		b, err := fs.ReadFile(panelRoot, p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "/?panel=classic") {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the panel: %v", err)
	}
	if !found {
		t.Error("the new panel offers no way back to the finished one")
	}
}
