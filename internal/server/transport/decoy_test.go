package transport

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The whole point of deriving the decoy from the token: two servers must not
// present the same identity, or the fleet is findable with one scan for one
// exact response.
func TestDecoyProfileDiffersBetweenInstalls(t *testing.T) {
	seen := map[string]string{}
	for i := 0; i < 200; i++ {
		token := fmt.Sprintf("token-%d-0123456789abcdef", i)
		p := newDecoyProfile(token)
		id := p.server + "|" + p.etag() + "|" + strconv.Itoa(len(p.page))

		if other, dup := seen[id]; dup {
			t.Fatalf("tokens %q and %q produce the identical decoy identity %s", other, token, id)
		}
		seen[id] = token
	}
}

// And the opposite half: one server keeps its identity. A file whose date and
// ETag change every time the machine reboots is not a file.
func TestDecoyProfileIsStableForOneToken(t *testing.T) {
	a := newDecoyProfile("a-stable-token-0123456789abcdef")
	b := newDecoyProfile("a-stable-token-0123456789abcdef")

	if a.server != b.server || a.etag() != b.etag() || a.page != b.page {
		t.Errorf("the same token produced two identities:\n %s / %s\n %s / %s",
			a.server, a.etag(), b.server, b.etag())
	}
}

// The ETag is made of the two headers beside it, so it has to agree with them.
// A random ETag next to its own Last-Modified and Content-Length is a shape
// nothing serving files produces.
func TestDecoyETagMatchesItsOwnHeaders(t *testing.T) {
	p := newDecoyProfile("etag-token-0123456789abcdef")
	want := fmt.Sprintf(`"%x-%x"`, p.modTime.Unix(), len(p.page))

	if p.etag() != want {
		t.Errorf("ETag = %s, want %s", p.etag(), want)
	}
}

// A Last-Modified after the response's own Date is one of the few things a
// static file genuinely cannot do, so the date must always land in the past.
func TestDecoyModTimeIsInThePast(t *testing.T) {
	for i := 0; i < 200; i++ {
		p := newDecoyProfile(fmt.Sprintf("past-%d", i))
		if !p.modTime.Before(time.Now()) {
			t.Fatalf("token %d dates index.html at %s, which is not in the past", i, p.modTime)
		}
	}
}

// A version string and the pages that version ships have to agree — nginx
// changed both in the 1.23 series, so an old version serving the new markup is
// an internal contradiction a fingerprinter can read off a single response.
func TestDecoyPagesMatchTheirBuild(t *testing.T) {
	for _, b := range nginxBuilds {
		classicIndex := b.page == indexClassic
		classicError := b.notFound == notFoundClassic

		if classicIndex != classicError {
			t.Errorf("nginx/%s mixes eras: classic index=%v, classic 404=%v",
				b.version, classicIndex, classicError)
		}
	}
}

// The index has to answer like a file on disk, not like a program with one
// canned reply — that means the full static-file header set.
func TestDecoyIndexLooksLikeAStaticFile(t *testing.T) {
	p := newDecoyProfile("index-token-0123456789abcdef")

	rec := httptest.NewRecorder()
	p.serve(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / returned %d, want 200", resp.StatusCode)
	}
	for _, h := range []string{"Server", "ETag", "Last-Modified", "Accept-Ranges", "Content-Length"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("%s is missing — a real file server sends it", h)
		}
	}
	if got := resp.Header.Get("Server"); !strings.HasPrefix(got, "nginx") {
		t.Errorf("Server = %q, want an nginx identity", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Welcome to nginx") {
		t.Errorf("the body is not the welcome page: %.80q", body)
	}
}

// A probe that hands back the ETag it was just given must get a 304. Answering
// 200 again says the page is generated rather than read off a disk.
func TestDecoyHonoursConditionalRequests(t *testing.T) {
	p := newDecoyProfile("conditional-token-0123456789abcd")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", p.etag())

	rec := httptest.NewRecorder()
	p.serve(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match with the current ETag returned %d, want 304", rec.Code)
	}
}

// Stock nginx serves the index at / and a 404 everywhere else. Returning the
// welcome page for every path anybody asks for is not something a static site
// does, and it was the older behaviour here.
func TestDecoyReturns404OffTheIndex(t *testing.T) {
	p := newDecoyProfile("notfound-token-0123456789abcdef")

	rec := httptest.NewRecorder()
	p.serve(rec, httptest.NewRequest(http.MethodGet, "/does-not-exist", nil))
	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /does-not-exist returned %d, want 404", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "404 Not Found") {
		t.Errorf("the 404 body is not nginx's: %.80q", body)
	}
	// nginx prints the same string in the header and in the error page footer;
	// a mismatch between the two would stand out.
	if !strings.Contains(string(body), resp.Header.Get("Server")) {
		t.Errorf("the 404 footer does not match Server: %q vs %.80q",
			resp.Header.Get("Server"), body)
	}
}

// The tunnel's own path must be a 404 among 404s. It used to be the one path
// that answered 200 alongside /, which made it the interesting one to try.
func TestDecoyDoesNotSingleOutTheTunnelPath(t *testing.T) {
	p := newDecoyProfile("tunnelpath-token-0123456789abcd")

	snapshot := func(path string) string {
		rec := httptest.NewRecorder()
		p.serve(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return fmt.Sprintf("%d|%s", rec.Code, rec.Body.String())
	}

	if got, want := snapshot("/channel"), snapshot("/some-other-path"); got != want {
		t.Errorf("/channel answers differently from an ordinary missing path:\n got %.120q\nwant %.120q", got, want)
	}
}
