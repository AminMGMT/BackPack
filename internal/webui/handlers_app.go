package webui

import (
	"net/http"

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

// The two files below make the panel installable as an app on a phone — the
// usual way this dashboard gets checked. Static, tiny, and served without
// auth because the browser asks for them before any login exists.

var manifestJSON = []byte(`{
  "name": "Backpack Panel",
  "short_name": "Backpack",
  "start_url": "/",
  "display": "standalone",
  "background_color": "#0a0a0b",
  "theme_color": "#0a0a0b",
  "icons": [
    { "src": "/icon.svg", "sizes": "any", "type": "image/svg+xml", "purpose": "any" }
  ]
}`)

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
	w.Write(manifestJSON)
}

func handleIcon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write(iconSVG)
}
