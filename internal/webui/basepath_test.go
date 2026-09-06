package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The panel answers under one unguessable path and nowhere else.
//
// A panel on a known port at "/" is found by the sweeps within hours of being
// started, and answers login attempts from strangers from then on. The password
// is still the thing that lets anybody in — this is not authentication and does
// not replace it. What it changes is who ever reaches the prompt.

// Nothing outside the path is served, and nothing about the reply says a panel
// is here.
func TestNothingIsServedOutsideTheBasePath(t *testing.T) {
	reached := false
	h := withBasePath("/secretpath", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))

	for _, path := range []string{
		"/", "/login", "/api/stats", "/manifest.json", "/sw.js", "/panel/",
		"/admin", "/secretpath2", "/notsecretpath", "/x/secretpath",
	} {
		reached = false
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
		if reached {
			t.Errorf("GET %s reached the panel", path)
		}
		// A reply that said "wrong path" would confirm there is a panel here.
		if body := w.Body.String(); strings.Contains(strings.ToLower(body), "panel") ||
			strings.Contains(body, "secretpath") {
			t.Errorf("GET %s gave the game away: %q", path, body)
		}
	}
}

// Everything under it is served, with the prefix stripped, so every handler
// sees the path it was written for.
func TestEverythingUnderTheBasePathIsServed(t *testing.T) {
	var saw string
	h := withBasePath("/secretpath", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saw = r.URL.Path
	}))

	for _, tc := range []struct{ asked, want string }{
		{"/secretpath/", "/"},
		{"/secretpath/login", "/login"},
		{"/secretpath/api/tunnel/edit", "/api/tunnel/edit"},
		{"/secretpath/css/tokens.css", "/css/tokens.css"},
	} {
		saw = ""
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", tc.asked, nil))
		if saw != tc.want {
			t.Errorf("GET %s reached the mux as %q, want %q", tc.asked, saw, tc.want)
		}
	}

	// The query survives the strip: half the panel's reads carry one.
	saw = ""
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/secretpath/api/logs?name=x&end=peer", nil))
	if saw != "/api/logs" {
		t.Errorf("path came through as %q", saw)
	}
}

// The bare prefix is the front door, and needs the trailing slash for the
// page's relative assets to resolve inside it.
func TestTheBarePrefixLeadsToThePanel(t *testing.T) {
	h := withBasePath("/secretpath", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/secretpath", nil))
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("GET /secretpath = %d, want a redirect", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/secretpath/" {
		t.Errorf("Location = %q, want /secretpath/", got)
	}
}

// An empty prefix is the panel at the root, unchanged.
func TestAnEmptyBasePathServesEverythingAsBefore(t *testing.T) {
	reached := false
	h := withBasePath("", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/stats", nil))
	if !reached {
		t.Error("a panel with no base path stopped serving its own routes")
	}
}

// Every panel gets one, including one that has been running at the root for a
// year — that is what "on by default" has to mean for an upgrade.
func TestABasePathIsGeneratedForAConfigThatHasNone(t *testing.T) {
	for i := 0; i < 8; i++ {
		p := randomPathSegment()
		if len(p) < 12 {
			t.Fatalf("generated %q — too short to be unguessable", p)
		}
		if !validBasePath(p) {
			t.Errorf("generated %q, which the panel would refuse to serve", p)
		}
		// Nothing that is misread when it is copied off a terminal by hand.
		if strings.ContainsAny(p, "0O1lI") {
			t.Errorf("generated %q, which contains a character that is misread when "+
				"somebody writes the address down", p)
		}
	}
	// Two panels must not land on the same path.
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p := randomPathSegment()
		if seen[p] {
			t.Fatal("randomPathSegment repeated itself")
		}
		seen[p] = true
	}
}

// PathPrefix and URL are what everything that shows the address is built from.
func TestTheAddressIncludesThePath(t *testing.T) {
	c := Config{Port: 7777, BasePath: "x7Kq2p"}
	if got := c.PathPrefix(); got != "/x7Kq2p" {
		t.Errorf("PathPrefix = %q", got)
	}
	if got := c.URL("203.0.113.9"); got != "http://203.0.113.9:7777/x7Kq2p/" {
		t.Errorf("URL = %q — an address without the path is one that 404s", got)
	}

	// Written with slashes, or with none, is the same path.
	for _, raw := range []string{"x7Kq2p", "/x7Kq2p", "x7Kq2p/", "/x7Kq2p/"} {
		if got := (Config{BasePath: raw}).PathPrefix(); got != "/x7Kq2p" {
			t.Errorf("BasePath %q gave prefix %q", raw, got)
		}
	}

	// "/" is how it is turned off.
	off := Config{Port: 7777, BasePath: "/"}
	if got := off.PathPrefix(); got != "" {
		t.Errorf("a base path of / gave prefix %q, want the root", got)
	}
	if got := off.URL("203.0.113.9"); got != "http://203.0.113.9:7777/" {
		t.Errorf("URL = %q", got)
	}
}

// A path the panel cannot serve is refused rather than stored.
func TestOnlyAPlainSegmentIsAcceptedAsABasePath(t *testing.T) {
	for _, bad := range []string{"a/b", "..", "a b", "a?b", "a#b", "a%2fb", "<script>"} {
		if validBasePath(bad) {
			t.Errorf("%q was accepted as a base path", bad)
		}
	}
	for _, ok := range []string{"x7Kq2p", "a-b_c", "ABC123", "/x7Kq2p/", "", "/"} {
		if !validBasePath(ok) {
			t.Errorf("%q was refused", ok)
		}
	}
}

// The pages carry the placeholder, and it is replaced with the real prefix.
func TestTheServedPagesAreStampedWithTheBasePath(t *testing.T) {
	loadPanel()

	if !strings.Contains(string(panelIndex), basePlaceholder) {
		t.Error("the panel shell carries no base-path placeholder, so its API calls " +
			"and its logout link point at the root, where nothing answers")
	}
	if !strings.Contains(string(loginHTML), basePlaceholder) {
		t.Error("the login page carries no base-path placeholder, so its form posts " +
			"to the root")
	}

	stamped := string(withBase(panelIndex, "/x7Kq2p"))
	if strings.Contains(stamped, basePlaceholder) {
		t.Error("the placeholder was left in the page")
	}
	if !strings.Contains(stamped, `data-base="/x7Kq2p"`) {
		t.Error("the shell does not tell the panel where it is served from")
	}
	if !strings.Contains(stamped, `href="/x7Kq2p/logout"`) {
		t.Error("the logout link does not carry the path")
	}

	// At the root the pages come out exactly as they were written.
	if root := string(withBase(panelIndex, "")); !strings.Contains(root, `href="/logout"`) {
		t.Error("a panel at the root did not produce its ordinary links")
	}
}

// The panel's only caller of fetch has to add the prefix, or nothing it asks
// for is under the path the server answers on.
func TestThePanelAsksUnderThePathItIsServedFrom(t *testing.T) {
	loadPanel()

	src, err := readPanelFile("js/api.js")
	if err != nil {
		t.Fatalf("api.js: %v", err)
	}
	js := string(src)
	if !strings.Contains(js, "dataset.base") {
		t.Fatal("api.js does not read where the panel is served from")
	}
	for _, call := range []string{"fetch(at(path)", "at('/api/tunnel/adopt"} {
		if !strings.Contains(js, call) {
			t.Errorf("a request is built without the base path (%q missing)", call)
		}
	}
	// And no view may call fetch on its own, which would bypass this.
	for _, f := range []string{"js/views/dashboard.js", "js/views/servers.js", "js/views/settings.js"} {
		b, err := readPanelFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if strings.Contains(string(b), "fetch('/") || strings.Contains(string(b), "fetch(`/") {
			t.Errorf("%s calls fetch with an absolute path, which ignores the base path", f)
		}
	}
}

func readPanelFile(name string) ([]byte, error) {
	loadPanel()
	return fs.ReadFile(panelRoot, name)
}

// "/" is how the path is turned off, and it has to survive a restart.
//
// The field is generated whenever it is empty, so an operator who wants the
// panel at the root cannot express that by clearing it — the next start would
// hand them a new path. "/" is the answer, and the thing that must not happen
// is EnsurePassword quietly treating it as "none set".
func TestServingAtTheRootIsRememberedRatherThanRegenerated(t *testing.T) {
	if got := (Config{BasePath: "/"}).PathPrefix(); got != "" {
		t.Fatalf("PathPrefix = %q, want the root", got)
	}
	// The generator only fires on an empty field.
	for _, set := range []string{"/", "x7Kq2p"} {
		c := Config{Password: "12345678", BasePath: set}
		if c.BasePath == "" {
			t.Fatalf("%q read back as empty", set)
		}
	}
	// And SetBasePath writes "/" rather than "", which would read as unset.
	if !validBasePath("/") {
		t.Error(`"/" is refused, so the panel cannot be put back at the root`)
	}
}

// A path the operator chose is kept as given, and one that could not be served
// is refused without touching what is stored.
func TestSettingAPathRefusesWhatItCannotServe(t *testing.T) {
	for _, bad := range []string{"a/b", "..", "a b", "a?b", "%2e%2e"} {
		if validBasePath(bad) {
			t.Errorf("%q would be accepted, and it is not a path segment", bad)
		}
	}
}
