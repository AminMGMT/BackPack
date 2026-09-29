package webui

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The panel's files went out with no ETag and no compression, so every visit
// downloaded all of them again. A first load gets them gzipped with an ETag; a
// return visit gets a 304 and no body.
func TestThePanelFilesAreCompressedAndRevalidated(t *testing.T) {
	loadPanel()
	want, err := fs.ReadFile(panelRoot, "js/main.js")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/js/main.js", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	rec := httptest.NewRecorder()
	servePanelAsset(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("first load: %d, encoding %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag, so a browser has nothing to revalidate with")
	}
	if rec.Body.Len() >= len(want) {
		t.Errorf("gzipped %d bytes is not smaller than %d", rec.Body.Len(), len(want))
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if !bytes.Equal(got, want) {
		t.Fatal("the gzipped file is not the file")
	}

	req = httptest.NewRequest("GET", "/js/main.js", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	servePanelAsset(rec, req)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("return visit: %d with %d bytes, want a bare 304", rec.Code, rec.Body.Len())
	}

	req = httptest.NewRequest("GET", "/js/main.js", nil)
	rec = httptest.NewRecorder()
	servePanelAsset(rec, req)
	if rec.Header().Get("Content-Encoding") != "" || !bytes.Equal(rec.Body.Bytes(), want) {
		t.Error("a browser that does not take gzip was sent it")
	}

	for _, p := range []string{"/nope.js", "/js/", "/../panel.go"} {
		rec = httptest.NewRecorder()
		servePanelAsset(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, rec.Code)
		}
	}
}
