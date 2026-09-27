package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The panel's own "Change password" sends JSON (api.js post() turns a plain
// object into a JSON body). The handler read only form fields, so the password
// always arrived empty and every change was refused as the wrong length.
func TestThePasswordCanBeChangedTheWayThePanelSendsIt(t *testing.T) {
	useConfigFile(t, Config{Password: "old-password"})
	s := loginServer()

	for _, tc := range []struct {
		name, ctype, body string
	}{
		{"json, as the panel sends it", "application/json", `{"password":"new-password-1"}`},
		{"form, as a script might", "application/x-www-form-urlencoded", "password=new-password-2"},
	} {
		r := httptest.NewRequest("POST", "/api/password", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", tc.ctype)
		w := httptest.NewRecorder()
		s.handlePassword(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: answered %d %q", tc.name, w.Code, strings.TrimSpace(w.Body.String()))
		}
		want := "new-password-1"
		if strings.HasPrefix(tc.name, "form") {
			want = "new-password-2"
		}
		if got := Load().Password; got != want {
			t.Fatalf("%s: the stored password is %q, want %q", tc.name, got, want)
		}
	}
}

// The client half of the same contract: the panel must send the password in a
// shape the handler reads. A form body is what every other ParseForm endpoint
// gets from api.js.
func TestThePanelSendsThePasswordAsAForm(t *testing.T) {
	raw, err := readPanelFile("js/api.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	i := strings.Index(src, "export const setPassword")
	if i < 0 {
		t.Fatal("setPassword is gone from api.js")
	}
	line := src[i:]
	if j := strings.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	if !strings.Contains(line, "URLSearchParams") {
		t.Fatalf("setPassword does not send a form: %s", line)
	}
}
