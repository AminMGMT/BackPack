package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/manage"
)

// The endpoint is one feature in two directions: it hands the paired settings
// over, and it takes them from the other side. What matters here is that a bad
// paste reaches the operator as the decoder's own words — a copy that stopped
// short, a version mismatch — rather than as a generic failure they cannot act
// on.

func TestPastingABadLinkSaysWhatIsWrong(t *testing.T) {
	srv := &server{sessions: newSessionStore()}

	for _, tc := range []struct{ name, link, want string }{
		{"empty", "", "paste the setup link"},
		{"not a link", "just some text", "does not look like"},
		{"truncated", "backpack://1.H4sIAAAA", "damaged"},
		{"a future version", "backpack://9.abc", "version 9"},
	} {
		body, _ := json.Marshal(map[string]string{"link": tc.link})
		r := httptest.NewRequest("POST", "/api/tunnel/sharelink", strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		srv.handleShareLink(w, r)

		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", tc.name, w.Code)
			continue
		}
		if !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s: %q does not tell the operator what happened (want %q)",
				tc.name, strings.TrimSpace(w.Body.String()), tc.want)
		}
	}
}

// A good link comes back as the other side's form, with the paired fields
// named — that list is what the panel warns on, and it has to arrive.
func TestPastingAGoodLinkReturnsTheMirroredForm(t *testing.T) {
	link, err := manage.ShareLink{
		Kind: "reverse", From: "iran", Name: "iran-a",
		Tok: "a-real-looking-token-0123456789abcdef", Tr: "tcp",
		Port: "8443", Host: "203.0.113.9", Preset: "turbo",
	}.Encode()
	if err != nil {
		t.Fatal(err)
	}

	srv := &server{sessions: newSessionStore()}
	body, _ := json.Marshal(map[string]string{"link": link})
	r := httptest.NewRequest("POST", "/api/tunnel/sharelink", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	srv.handleShareLink(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var form manage.PeerForm
	if err := json.Unmarshal(w.Body.Bytes(), &form); err != nil {
		t.Fatalf("the reply is not a form: %v", err)
	}
	if form.Side != "kharej" || form.Transport != "tcp" || form.Token == "" {
		t.Errorf("the form did not arrive filled: %+v", form)
	}
	if form.ServerAddr != "203.0.113.9" {
		t.Errorf("the kharej side was not told where to dial: %q", form.ServerAddr)
	}
	if len(form.Paired) == 0 {
		t.Error("no fields were marked paired, so the panel would warn about nothing")
	}
}

// Asking for a link for a tunnel that does not exist is a mistake to report,
// not something to answer with an empty link the operator would then paste.
func TestAskingForALinkForNothingIsRefused(t *testing.T) {
	srv := &server{sessions: newSessionStore()}
	for _, q := range []string{"", "?name=does-not-exist"} {
		r := httptest.NewRequest("GET", "/api/tunnel/sharelink"+q, nil)
		w := httptest.NewRecorder()
		srv.handleShareLink(w, r)
		if w.Code == http.StatusOK {
			t.Errorf("%q was answered with a link", q)
		}
	}
}

func TestShareLinkRejectsOtherMethods(t *testing.T) {
	srv := &server{sessions: newSessionStore()}
	r := httptest.NewRequest("DELETE", "/api/tunnel/sharelink", nil)
	w := httptest.NewRecorder()
	srv.handleShareLink(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status %d, want 405", w.Code)
	}
}
