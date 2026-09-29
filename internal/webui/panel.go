package webui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/backpack/backpack/internal/app"
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
	panelOnce  sync.Once
	panelRoot  fs.FS
	panelIndex []byte
)

// loadPanel prepares the embedded panel once, on first use.
func loadPanel() {
	panelOnce.Do(func() {
		panelRoot, _ = fs.Sub(panelFS, "panel")
		raw, _ := fs.ReadFile(panelRoot, "index.html")
		panelIndex = raw
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
		// The shell carries one inline script; the nonce in this response's
		// CSP is what lets it run. See withNonce in panelsecurity.go.
		w.Write(withNonce(withBase(panelIndex, basePrefix()), r))
	default:
		servePanelAsset(w, r)
	}
}

// Serving the panel's files.
//
// They used to go through http.FileServerFS, and an embedded file has no
// modification time — so no Last-Modified and no ETag went out, a browser had
// nothing to revalidate with, and every visit downloaded all 900 KB of the
// panel again, uncompressed. From Iran, over the route a panel is usually
// reached by, that was much of why the panel felt slow. Each file now carries
// an ETag made of the version and its content, so a return visit costs one
// small 304 per file, and the text files go gzipped to a browser that takes
// it. Both are worked out once per file and kept.

type panelAsset struct {
	raw, gz []byte
	etag    string
	ctype   string
}

var (
	assetMu sync.Mutex
	assets  = map[string]*panelAsset{}
)

// assetFor is the prepared file at name, or nil when there is none.
func assetFor(name string) *panelAsset {
	assetMu.Lock()
	defer assetMu.Unlock()
	if a, ok := assets[name]; ok {
		return a
	}
	raw, err := fs.ReadFile(panelRoot, name)
	if err != nil {
		assets[name] = nil
		return nil
	}
	sum := sha256.Sum256(raw)
	a := &panelAsset{
		raw:   raw,
		etag:  `"` + app.Version + "-" + hex.EncodeToString(sum[:8]) + `"`,
		ctype: mime.TypeByExtension(path.Ext(name)),
	}
	if a.ctype == "" {
		a.ctype = "application/octet-stream"
	}
	if strings.HasPrefix(a.ctype, "text/") || strings.Contains(a.ctype, "javascript") ||
		strings.Contains(a.ctype, "json") || strings.Contains(a.ctype, "svg") {
		var b bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		if b.Len() < len(raw) {
			a.gz = b.Bytes()
		}
	}
	assets[name] = a
	return a
}

func servePanelAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	a := assetFor(name)
	if name == "" || a == nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("ETag", a.etag)
	// Revalidated on every load — which with the ETag is a 304 — so an update
	// is picked up at once and nothing stale is kept.
	h.Set("Cache-Control", "no-cache")
	h.Set("Vary", "Accept-Encoding")
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, a.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", a.ctype)
	body := a.raw
	if a.gz != nil && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		body = a.gz
	}
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
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
	redirectTo(w, r, target, http.StatusMovedPermanently)
}
