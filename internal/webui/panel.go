package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"sync"
)

// The panel.
//
// This is the whole web UI. It used to be the second of two: a rebuilt panel
// served under /panel/ beside the single-file dashboard in assets, with a
// per-server setting deciding which of them "/" opened. That arrangement was
// scaffolding for the rebuild — a way to ship an unfinished panel without
// taking the finished one away — and it is gone now that the rebuild is done.
// The dashboard, the setting, and the escape hatch back to it went with it.
//
// What is left is the ordinary case: this panel is served at "/", and the
// assets it asks for relatively resolve against the root. /panel/ still
// answers, with a redirect, because operators bookmarked it while the two
// panels lived side by side.
//
// Only what the browser actually loads is embedded. mock/ is the preview's
// fixture data and serve.py is its dev server; shipping either would put a
// second, fake source of truth inside the binary.
//
//go:embed panel/index.html panel/css panel/js panel/views
var panelFS embed.FS

// panelPrefix is where the panel used to live. It redirects to "/" now.
const panelPrefix = "/panel/"

var (
	panelOnce   sync.Once
	panelRoot   fs.FS
	panelIndex  []byte
	panelServer http.Handler
)

// loadPanel prepares the embedded panel once, on first use.
func loadPanel() {
	panelOnce.Do(func() {
		panelRoot, _ = fs.Sub(panelFS, "panel")
		raw, _ := fs.ReadFile(panelRoot, "index.html")
		panelIndex = raw
		panelServer = http.FileServerFS(panelRoot)
	})
}

// handlePanel serves the panel and everything it loads.
//
// It is registered at "/", so it is also where every path that matched no
// other route arrives. The file server answers those the only way it can, with
// a 404 — which is what they were getting before, from a different handler.
func (s *server) handlePanel(w http.ResponseWriter, r *http.Request) {
	loadPanel()
	if len(panelIndex) == 0 {
		http.Error(w, "the panel is not available in this build", http.StatusNotFound)
		return
	}
	switch r.URL.Path {
	case "/", "/index.html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(panelIndex)
	default:
		panelServer.ServeHTTP(w, r)
	}
}

// handleOldPanelPath keeps /panel/ bookmarks working.
//
// The panel answered there for as long as there were two of them, which is
// long enough for the address to be saved, sent to someone, or left open in a
// pinned tab. A redirect costs one round trip and means none of those break.
func (s *server) handleOldPanelPath(w http.ResponseWriter, r *http.Request) {
	target := "/"
	if rest := r.URL.Path[len(panelPrefix)-1:]; rest != "/" {
		target = rest
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	if r.URL.Fragment != "" {
		target += "#" + r.URL.Fragment
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}
