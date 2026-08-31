package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"sync"
)

// The second panel, and the switch between the two.
//
// internal/webui/panel is a rebuilt web UI being written alongside the one in
// assets/dashboard.html. It is not finished, so it is not what a fresh install
// gets: the dashboard keeps serving "/" exactly as it always has, and this one
// is opt-in, per server, from Settings → Interface.
//
// Two rules make that opt-in safe to offer while the new panel is still moving:
//
//   - The two are served from different paths. The classic panel stays at "/",
//     the new one lives under /panel/, and turning the setting on only decides
//     which of them "/" sends a browser to. Nothing about the old panel changes.
//
//   - There is a way back that needs no working JavaScript. A panel that is
//     still being built can fail in ways that leave no button to press, and an
//     operator whose only route back is a control inside the broken page has no
//     route back at all. GET /?panel=classic returns them to the dashboard and
//     records the choice, so the next visit lands there too.
//
// Only what the browser actually loads is embedded. mock/ is the preview's
// fixture data and serve.py is its dev server; shipping either would put a
// second, fake source of truth inside the binary.
//
//go:embed panel/index.html panel/css panel/js panel/views
var experimentalPanelFS embed.FS

// panelPrefix is where the new panel is served. The trailing slash matters:
// index.html asks for its assets relatively ("css/tokens.css", "js/main.js"),
// so the page has to be reached at a directory rather than at a bare "/panel".
// net/http's mux redirects the bare form here on its own.
const panelPrefix = "/panel/"

// liveMarker is what tells the new panel it is talking to a real server.
//
// Its api.js reads window.__BACKPACK_LIVE__ and, without it, serves every
// screen from mock/*.json — which is right when the page is opened from the
// static preview and would be a lie here. The panel's README states that the Go
// handler is what sets it; this is that.
const liveMarker = `<script>window.__BACKPACK_LIVE__=true;</script>`

var (
	panelOnce   sync.Once
	panelRoot   fs.FS
	panelIndex  []byte
	panelServer http.Handler
)

// loadExperimentalPanel prepares the embedded panel once, on first use. A
// server that never has the setting turned on never pays for it.
func loadExperimentalPanel() {
	panelOnce.Do(func() {
		panelRoot, _ = fs.Sub(experimentalPanelFS, "panel")
		raw, _ := fs.ReadFile(panelRoot, "index.html")
		panelIndex = withLiveMarker(raw)
		panelServer = http.StripPrefix(panelPrefix, http.FileServerFS(panelRoot))
	})
}

// withLiveMarker puts the marker in the page's head, ahead of the module that
// reads it. Appending it to a page with no head at all would run it after
// js/main.js has already imported api.js and decided, so that case is prefixed
// instead of appended.
func withLiveMarker(page []byte) []byte {
	const head = "</head>"
	i := bytes.Index(page, []byte(head))
	if i < 0 {
		return append([]byte(liveMarker), page...)
	}
	out := make([]byte, 0, len(page)+len(liveMarker))
	out = append(out, page[:i]...)
	out = append(out, liveMarker...)
	return append(out, page[i:]...)
}

// handleExperimentalPanel serves the new panel and everything it loads.
//
// It answers whether or not the setting is on. The setting decides where "/"
// sends a browser; it is not a lock on the path, so somebody can look at the
// new panel once without committing the server to it, and a browser left on a
// /panel/ bookmark is not bounced away from the page it asked for.
func (s *server) handleExperimentalPanel(w http.ResponseWriter, r *http.Request) {
	loadExperimentalPanel()
	if len(panelIndex) == 0 {
		http.Error(w, "the new panel is not available in this build", http.StatusNotFound)
		return
	}
	switch r.URL.Path {
	case panelPrefix, panelPrefix + "index.html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(panelIndex)
	default:
		panelServer.ServeHTTP(w, r)
	}
}

// choosePanel records which panel this server shows at "/" and sends the
// browser to it.
//
// It is a GET because of what it is for: the escape hatch has to be something
// an operator can type into the address bar when the page they are looking at
// has stopped responding to clicks. /logout is a GET here for the same reason.
// What a request forged from another site could achieve is swapping which of
// two local panels this operator sees next, which they can see and undo in one
// click — so the property being traded away is worth less than the recovery it
// buys.
func (s *server) choosePanel(w http.ResponseWriter, r *http.Request, choice string) {
	want := choice == "next"
	c := Load()
	if c.ExperimentalPanel != want {
		c.ExperimentalPanel = want
		// A failed write is not worth an error page: the redirect below still
		// takes them where they asked to go, and the choice simply does not
		// outlive the visit.
		_ = Save(c)
	}
	if want {
		http.Redirect(w, r, panelPrefix, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handlePanelUI reads (GET) or sets (POST) which panel this server shows.
func (s *server) handlePanelUI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"experimental": Load().ExperimentalPanel})
	case http.MethodPost:
		r.ParseForm()
		on := r.FormValue("experimental") == "1"
		c := Load()
		c.ExperimentalPanel = on
		if err := Save(c); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"experimental": on})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
