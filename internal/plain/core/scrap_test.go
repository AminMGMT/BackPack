package core

import (
	"bufio"
	"bytes"
	"net/http"
	"testing"
)

func scrapReq(t *testing.T, token string) []byte {
	t.Helper()
	b, err := scrapRequest(token, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseReq(t *testing.T, raw []byte) *http.Request {
	t.Helper()
	r, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The cookie proves the token once: the same request again is refused, and
// another token's never passes.
func TestScrapCookieIsOneUseAndNeedsTheToken(t *testing.T) {
	raw := scrapReq(t, "tok")
	if !scrapAuthorised(parseReq(t, raw), "tok") {
		t.Fatal("a fresh request was refused")
	}
	if scrapAuthorised(parseReq(t, raw), "tok") {
		t.Fatal("a replayed request was accepted")
	}
	if scrapAuthorised(parseReq(t, scrapReq(t, "other")), "tok") {
		t.Fatal("another token's request was accepted")
	}
}

// The request reads as a browser's: a POST with Chrome's agent and a chunked
// body, nothing that names Backpack.
func TestScrapRequestLooksOrdinary(t *testing.T) {
	r := parseReq(t, scrapReq(t, "tok"))
	if r.Method != http.MethodPost || r.Host != "192.0.2.1" || r.TransferEncoding[0] != "chunked" {
		t.Fatalf("request %s %s %v", r.Method, r.Host, r.TransferEncoding)
	}
	if ua := r.UserAgent(); !bytes.Contains([]byte(ua), []byte("Chrome/")) {
		t.Fatalf("user agent %q", ua)
	}
	if bytes.Contains(bytes.ToLower(scrapReq(t, "tok")), []byte("backpack")) {
		t.Fatal("the request names Backpack")
	}
}
