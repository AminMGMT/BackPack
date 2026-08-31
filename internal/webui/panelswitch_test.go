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

// The new panel decides between the real API and its fixtures by reading
// window.__BACKPACK_LIVE__, and the Go handler is the only thing that sets it.
// If the panel ever renames that flag, every screen a real operator opens would
// quietly show somebody else's numbers — a panel that looks perfectly healthy
// and is describing a machine that does not exist. So the two halves are pinned
// against each other rather than each on its own.
func TestTheLiveFlagTheHandlerSetsIsTheOneThePanelReads(t *testing.T) {
	loadExperimentalPanel()

	api, err := fs.ReadFile(panelRoot, "js/api.js")
	if err != nil {
		t.Fatalf("the panel's api.js is not embedded: %v", err)
	}
	if !strings.Contains(string(api), "window.__BACKPACK_LIVE__") {
		t.Error("api.js no longer reads window.__BACKPACK_LIVE__, so the served panel " +
			"will fall back to its mock fixtures against a real server")
	}
	if !strings.Contains(liveMarker, "window.__BACKPACK_LIVE__") {
		t.Error("the marker the handler injects does not set the flag api.js reads")
	}
}

// The marker has to land in the head, ahead of the module that reads it, and
// exactly once.
func TestTheLiveMarkerGoesInTheHead(t *testing.T) {
	page := []byte("<html><head><title>x</title></head><body>b</body></html>")
	got := string(withLiveMarker(page))

	if strings.Count(got, liveMarker) != 1 {
		t.Fatalf("the marker appears %d times: %s", strings.Count(got, liveMarker), got)
	}
	if strings.Index(got, liveMarker) > strings.Index(got, "</head>") {
		t.Error("the marker is after the head; the module reads the flag before it is set")
	}

	// A page with no head at all still has to have the flag set before its
	// scripts run, so it is prefixed rather than appended.
	bare := string(withLiveMarker([]byte("<body><script src=x></script></body>")))
	if !strings.HasPrefix(bare, liveMarker) {
		t.Error("a page with no head did not get the marker first")
	}
}

// What is served at /panel/ is the panel with the flag in it, and the assets it
// asks for resolve — index.html names them relatively, so the trailing slash on
// the prefix is load-bearing.
func TestTheNewPanelServesItselfAndItsAssets(t *testing.T) {
	srv := &server{sessions: newSessionStore()}

	w := httptest.NewRecorder()
	srv.handleExperimentalPanel(w, httptest.NewRequest("GET", panelPrefix, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", panelPrefix, w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, liveMarker) {
		t.Error("the page was served without the live flag; it would show mock data")
	}
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

	loadExperimentalPanel()
	menu, err := fs.ReadFile(panelRoot, "js/ui/menu.js")
	if err != nil {
		t.Fatalf("the panel's menu is not embedded: %v", err)
	}
	if !strings.Contains(string(menu), "/?panel=classic") {
		t.Error("the new panel offers no way back to the finished one")
	}
}
