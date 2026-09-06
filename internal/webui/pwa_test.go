package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The PWA surface: the three files a browser fetches before it will offer to
// install, all served without auth because the browser asks for them before a
// session exists.
func TestPWAAssets(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", handleManifest)
	mux.HandleFunc("/icon.svg", handleIcon)
	mux.HandleFunc("/icons/", handleIconPNG)
	mux.HandleFunc("/sw.js", handleServiceWorker)

	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}

	for _, tc := range []struct{ path, ctype string }{
		{"/manifest.json", "application/manifest+json"},
		{"/icon.svg", "image/svg+xml"},
		{"/icons/icon-192.png", "image/png"},
		{"/icons/icon-512.png", "image/png"},
		{"/icons/icon-maskable-512.png", "image/png"},
		{"/icons/apple-touch-icon.png", "image/png"},
		{"/sw.js", "text/javascript; charset=utf-8"},
	} {
		w := get(tc.path)
		if w.Code != 200 {
			t.Errorf("%s: status %d, want 200", tc.path, w.Code)
			continue
		}
		if got := w.Header().Get("Content-Type"); got != tc.ctype {
			t.Errorf("%s: content-type %q, want %q", tc.path, got, tc.ctype)
		}
		if w.Body.Len() == 0 {
			t.Errorf("%s: empty body", tc.path)
		}
	}

	// PNG magic — a truncated embed would still be served with a 200.
	for _, p := range []string{"/icons/icon-192.png", "/icons/apple-touch-icon.png"} {
		if b := get(p).Body.Bytes(); len(b) < 8 || string(b[1:4]) != "PNG" {
			t.Errorf("%s is not a PNG", p)
		}
	}

	// A worker that a browser caches is a worker a panel gets stuck on.
	if cc := get("/sw.js").Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("sw.js Cache-Control %q, want no-cache", cc)
	}
	if sa := get("/sw.js").Header().Get("Service-Worker-Allowed"); sa != "/" {
		t.Errorf("sw.js Service-Worker-Allowed %q, want /", sa)
	}

	// An unknown icon must 404 rather than fall through to something else.
	if w := get("/icons/nope.png"); w.Code != 404 {
		t.Errorf("unknown icon: status %d, want 404", w.Code)
	}

	// Chrome's install criteria, read straight off the manifest.
	var m struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
		StartURL  string `json:"start_url"`
		Scope     string `json:"scope"`
		Display   string `json:"display"`
		Icons     []struct {
			Src, Sizes, Type, Purpose string
		} `json:"icons"`
	}
	if err := json.Unmarshal(get("/manifest.json").Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if m.Name == "" || m.ShortName == "" {
		t.Error("manifest needs both name and short_name")
	}
	// Relative, not "/". The panel is served under an unguessable base path, so
	// every address in the manifest resolves against the manifest's own URL and
	// moves with it. An absolute "/" pointed every installed app at the root,
	// which is now the one place the panel does not answer.
	if m.StartURL != "./" || m.Display != "standalone" {
		t.Errorf("start_url %q display %q", m.StartURL, m.Display)
	}
	if m.Scope != "./" {
		t.Errorf("scope %q — an absolute scope leaves the installed app pointing "+
			"outside the path the panel is served under", m.Scope)
	}
	var big, maskable bool
	for _, ic := range m.Icons {
		if ic.Sizes == "512x512" || ic.Sizes == "192x192" {
			big = true
		}
		if ic.Purpose == "maskable" {
			maskable = true
		}
		// Every icon the manifest names must actually be served. The paths are
		// relative to the manifest, which sits beside them at the panel's root.
		if w := get("/" + strings.TrimPrefix(ic.Src, "./")); w.Code != 200 {
			t.Errorf("manifest icon %s: status %d", ic.Src, w.Code)
		}
	}
	if !big {
		t.Error("manifest has no bitmap icon at an installable size")
	}
	if !maskable {
		t.Error("manifest has no maskable icon — Android will letterbox it")
	}
}
