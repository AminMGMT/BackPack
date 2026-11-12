package webui

import (
	"net/http"

	"github.com/backpack/backpack/internal/manage"
)

// handleShowcase answers what the overview's showcase box draws: the link,
// the title and the version of the image, or an empty object when there is
// nothing to draw. It reads the cache only, and starts a refresh when the
// cache is stale.
func (s *server) handleShowcase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	manage.KickShowcase()
	sc := manage.LoadShowcase()
	if !sc.Present() {
		writeJSON(w, struct{}{})
		return
	}
	writeJSON(w, map[string]string{"link": sc.Link, "title": sc.Title, "v": sc.Hash})
}

// handleShowcaseImage serves the cached image. The address carries its hash
// (?v=), so the browser may keep it as long as it likes: a new image is a new
// address.
func (s *server) handleShowcaseImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	img, typ, hash, err := manage.ShowcaseImage()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", typ)
	h.Set("X-Content-Type-Options", "nosniff")
	// Kept only under the address of the image it is. A page that asked for
	// an older hash gets the current bytes, and they must not be remembered
	// under the old name.
	if r.URL.Query().Get("v") == hash {
		h.Set("Cache-Control", "private, max-age=86400")
	} else {
		h.Set("Cache-Control", "no-store")
	}
	w.Write(img)
}
