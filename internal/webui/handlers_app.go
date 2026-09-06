package webui

import (
	_ "embed"
	"net/http"
	"strings"

	"github.com/backpack/backpack/internal/manage"
)

// handleChannel reads (GET) or switches (POST) the release channel the
// updater follows — the same setting as the CLI's Update → Release channel.
func (s *server) handleChannel(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]string{"channel": manage.Channel()})
	case http.MethodPost:
		r.ParseForm()
		ch := r.FormValue("channel")
		if err := manage.SetChannel(ch); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"channel": ch})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// The files below make the panel installable as an app on a phone — the usual
// way this dashboard gets checked. Static, tiny, and served without auth
// because the browser asks for them before any login exists. None of them
// carries data about the server, so there is nothing here to protect.
//
// Installing needs HTTPS. That is the browser's rule, not ours: a service
// worker is only registered in a secure context, and without one no browser
// offers to install. A panel on plain http:// therefore cannot be added to a
// home screen as an app, which is why the dashboard's install prompt sends the
// operator to the CLI's Panel certificate screen first.

// The manifest's paths are all relative to the manifest's own URL, so the whole
// document moves with the panel when it is served under a base path — and none
// of it has to know what that path is. An absolute "/" here would have pointed
// every installed app at the root, where the panel no longer answers.
var manifestJSON = []byte(`{
  "id": "./",
  "name": "Backpack Panel",
  "short_name": "Backpack",
  "description": "Live tunnel and server monitoring for Backpack.",
  "start_url": "./",
  "scope": "./",
  "display": "standalone",
  "display_override": ["standalone", "minimal-ui"],
  "orientation": "portrait-primary",
  "background_color": "#0a0a0b",
  "theme_color": "#0a0a0b",
  "categories": ["utilities", "productivity"],
  "icons": [
    { "src": "./icons/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any" },
    { "src": "./icons/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any" },
    { "src": "./icons/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable" },
    { "src": "./icon.svg", "sizes": "any", "type": "image/svg+xml", "purpose": "any" }
  ]
}`)

// The bitmaps. The mark exists as SVG below and that is what desktop browsers
// use, but iOS ignores SVG for a home-screen icon and Android's maskable icons
// want a real bitmap with a safe zone, so both are shipped.
var (
	//go:embed assets/icons/icon-192.png
	icon192PNG []byte
	//go:embed assets/icons/icon-512.png
	icon512PNG []byte
	//go:embed assets/icons/icon-maskable-512.png
	iconMaskablePNG []byte
	//go:embed assets/icons/apple-touch-icon.png
	appleTouchPNG []byte
)

//go:embed assets/sw.js
var serviceWorkerJS []byte

// iconSVG is the header's backpack mark on the accent gradient, as a file.
var iconSVG = []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 128 128">
  <defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1">
    <stop offset="0" stop-color="#ff453a"/><stop offset="1" stop-color="#a12219"/>
  </linearGradient></defs>
  <rect width="128" height="128" rx="30" fill="url(#g)"/>
  <g fill="none" stroke="#fff" stroke-width="8" stroke-linecap="round" stroke-linejoin="round">
    <path d="M46 38v-4a18 18 0 0 1 36 0v4"/>
    <path d="M32 46h64a9 9 0 0 1 9 9v36a13 13 0 0 1-13 13H36a13 13 0 0 1-13-13V55a9 9 0 0 1 9-9z"/>
    <path d="M51 72h26"/>
  </g>
</svg>`)

func handleManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(manifestJSON)
}

func handleIcon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Write(iconSVG)
}

// handleIconPNG serves the bitmap icons. They never change within a build, so
// they are cached for a week; an update ships a new binary, and the manifest —
// cached for a day — is what points at them.
func handleIconPNG(w http.ResponseWriter, r *http.Request) {
	var body []byte
	switch strings.TrimPrefix(r.URL.Path, "/icons/") {
	case "icon-192.png":
		body = icon192PNG
	case "icon-512.png":
		body = icon512PNG
	case "icon-maskable-512.png":
		body = iconMaskablePNG
	case "apple-touch-icon.png":
		body = appleTouchPNG
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Write(body)
}

// handleServiceWorker serves the worker that makes the panel installable.
//
// Two headers matter here. Service-Worker-Allowed lets a worker served from
// anywhere control the whole origin — it is served from the root already, so
// this is belt and braces. no-cache is the important one: a stale worker is
// the classic way a web app gets stuck on an old version, and this file is a
// few hundred bytes, so re-validating it on every load costs nothing.
func handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Service-Worker-Allowed", "/")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(serviceWorkerJS)
}
