package manage

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func testGIF(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 4, 4), []color.Color{c})
	var b bytes.Buffer
	if err := gif.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// publisher is the developer's server: a manifest and the file it names,
// either of which a test can change or take away.
type publisher struct {
	manifest   atomic.Value // string; "" answers 404
	image      atomic.Value // []byte
	imageGets  atomic.Int32
	notChanged atomic.Int32
}

func newPublisher(t *testing.T) (*publisher, *httptest.Server) {
	p := &publisher{}
	p.manifest.Store(`{"image":"ad.gif","link":"https://t.me/example","title":"Example"}`)
	p.image.Store(testGIF(t, color.White))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bp/ad.json":
			m := p.manifest.Load().(string)
			if m == "" {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(m))
		case "/bp/ad.gif":
			p.imageGets.Add(1)
			img := p.image.Load().([]byte)
			etag := `"` + string(rune('a'+len(img)%26)) + `"`
			if r.Header.Get("If-None-Match") == etag {
				p.notChanged.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			w.Write(img)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	setShowcase(t, srv.URL+"/bp/ad.json")
	return p, srv
}

// setShowcase points the showcase at addr with an empty cache, for one test.
func setShowcase(t *testing.T, addr string) {
	oldURL, oldDir := ShowcaseURL, ShowcaseDir
	ShowcaseURL, ShowcaseDir = addr, t.TempDir()
	t.Cleanup(func() { ShowcaseURL, ShowcaseDir = oldURL, oldDir })
}

func TestShowcaseIsFetchedCachedAndServedFromDisk(t *testing.T) {
	_, srv := newPublisher(t)
	if err := RefreshShowcase(); err != nil {
		t.Fatal(err)
	}
	sc := LoadShowcase()
	if !sc.Present() || sc.Type != "image/gif" || sc.Link != "https://t.me/example" || sc.Title != "Example" {
		t.Fatalf("cached showcase = %+v", sc)
	}
	if sc.Image != srv.URL+"/bp/ad.gif" {
		t.Errorf("image resolved to %q, want it beside the manifest", sc.Image)
	}
	b, typ, hash, err := ShowcaseImage()
	if err != nil || typ != "image/gif" || hash != sc.Hash || !bytes.HasPrefix(b, []byte("GIF8")) {
		t.Fatalf("ShowcaseImage = %d bytes, %q, %q, %v", len(b), typ, hash, err)
	}

	// The publishing server going away is not the publisher withdrawing it.
	srv.Close()
	if err := RefreshShowcase(); err == nil {
		t.Error("a refresh against a closed server reported success")
	}
	if !LoadShowcase().Present() {
		t.Error("an unreachable publisher erased the cached showcase")
	}
}

func TestShowcaseUnchangedImageIsNotDownloadedAgain(t *testing.T) {
	p, _ := newPublisher(t)
	if err := RefreshShowcase(); err != nil {
		t.Fatal(err)
	}
	first := LoadShowcase().Hash
	if err := RefreshShowcase(); err != nil {
		t.Fatal(err)
	}
	if p.notChanged.Load() != 1 || LoadShowcase().Hash != first {
		t.Errorf("second refresh: %d not-modified answers, hash %q -> %q", p.notChanged.Load(), first, LoadShowcase().Hash)
	}

	// Replaced under the same name, it is noticed.
	p.image.Store(append(testGIF(t, color.Black), 0))
	if err := RefreshShowcase(); err != nil {
		t.Fatal(err)
	}
	if LoadShowcase().Hash == first {
		t.Error("a replaced image kept the old hash")
	}
}

func TestShowcaseWithdrawnByThePublisher(t *testing.T) {
	for name, withdraw := range map[string]func(*publisher){
		"manifest gone":    func(p *publisher) { p.manifest.Store("") },
		"image left empty": func(p *publisher) { p.manifest.Store(`{"image":""}`) },
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := newPublisher(t)
			if err := RefreshShowcase(); err != nil {
				t.Fatal(err)
			}
			withdraw(p)
			if err := RefreshShowcase(); err != nil {
				t.Fatal(err)
			}
			if LoadShowcase().Present() {
				t.Error("the showcase is still drawn after the publisher withdrew it")
			}
			if _, _, _, err := ShowcaseImage(); err == nil {
				t.Error("the withdrawn image is still served")
			}
		})
	}
}

func TestShowcaseRefusesWhatIsNotAPictureOrALink(t *testing.T) {
	p, _ := newPublisher(t)
	p.image.Store([]byte("<html><script>alert(1)</script></html>"))
	if err := RefreshShowcase(); err == nil || LoadShowcase().Present() {
		t.Errorf("an HTML file was accepted as the image (err %v)", err)
	}

	p.image.Store(testGIF(t, color.White))
	p.manifest.Store(`{"image":"ad.gif","link":"javascript:alert(1)"}`)
	if err := RefreshShowcase(); err != nil {
		t.Fatal(err)
	}
	if l := LoadShowcase().Link; l != "" {
		t.Errorf("link = %q, want a non-http link dropped", l)
	}

	p.manifest.Store(`{"image":"file:///etc/shadow"}`)
	if err := RefreshShowcase(); err == nil {
		t.Error("a file: image address was fetched")
	}
}

func TestShowcaseOffWithoutAnAddress(t *testing.T) {
	setShowcase(t, "")
	if err := RefreshShowcase(); err != nil || LoadShowcase().Present() {
		t.Errorf("with no address: err %v, present %v", err, LoadShowcase().Present())
	}
}
