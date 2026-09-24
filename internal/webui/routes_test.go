package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/alerthist"
)

// These go through the route table as Serve wires it, not through guard on its
// own. The guard was always right; what was wrong was which scope each route
// asked it for, and nothing tested that.

// adminRoutes is every endpoint that decides who can get in. Each one, reached
// with a write token, is a way to become the panel password.
var adminRoutes = []struct{ method, path string }{
	{"POST", "/api/tokens"},
	{"GET", "/api/audit"},
	{"POST", "/api/password"},
	{"POST", "/api/totp"},
	{"GET", "/api/sessions"},
	{"POST", "/api/sessions"},
	{"GET", "/api/backup/export"},
	{"POST", "/api/backup/import"},
	{"GET", "/api/telegram"},
	{"POST", "/api/telegram"},
	{"POST", "/api/panelport"},
	{"POST", "/api/panelcert"},
}

func isolateAlerts(t *testing.T) {
	t.Helper()
	old := alerthist.Dir
	alerthist.Dir = t.TempDir()
	t.Cleanup(func() { alerthist.Dir = old })
}

func TestAWriteTokenCannotReachAnythingThatGrantsAccess(t *testing.T) {
	isolateAccess(t)
	isolateAlerts(t)
	secret, _, err := IssueToken("ci", ScopeWrite, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mux := newServer().routes()
	for _, rt := range adminRoutes {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req(rt.method, rt.path, secret))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s with a write token answered %d, want 403", rt.method, rt.path, w.Code)
		}
	}
}

func TestAReadTokenCannotReachAnythingThatGrantsAccess(t *testing.T) {
	isolateAccess(t)
	isolateAlerts(t)
	secret, _, err := IssueToken("scraper", ScopeRead, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mux := newServer().routes()
	for _, rt := range adminRoutes {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req(rt.method, rt.path, secret))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s with a read token answered %d, want 403", rt.method, rt.path, w.Code)
		}
	}
}

// Every /api route refuses a caller with no credential. A route registered
// without a guard is the one this catches, and the list is read from the table
// rather than written out, so a new route is covered the day it is added.
func TestNoAPIRouteAnswersWithoutACredential(t *testing.T) {
	isolateAccess(t)
	isolateAlerts(t)
	mux := newServer().routes()
	for _, path := range registeredAPIPaths(t) {
		for _, method := range []string{"GET", "POST"} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req(method, path, ""))
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with no credential answered %d, want 401", method, path, w.Code)
			}
		}
	}
}

// The refusal says what was needed. It used to say "read-only" to a write
// token turned away from an admin endpoint.
func TestARefusalNamesTheScopes(t *testing.T) {
	isolateAccess(t)
	isolateAlerts(t)
	secret, _, err := IssueToken("ci", ScopeWrite, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	newServer().routes().ServeHTTP(w, req("POST", "/api/password", secret))
	if body := w.Body.String(); !strings.Contains(body, "write") || !strings.Contains(body, "admin") {
		t.Fatalf("refusal = %q, want it to name both scopes", body)
	}
}

// Changing the password or the second factor leaves the machine as it
// happens. The forwarding list named a route that does not exist.
func TestAccessChangesAreForwarded(t *testing.T) {
	for _, path := range []string{"/api/password", "/api/totp", "/api/sessions",
		"/api/telegram", "/api/backup/import", "/api/tokens"} {
		if !worthForwarding(auditEntry{Method: "POST", Path: path, Status: 200}) {
			t.Errorf("%s is not forwarded", path)
		}
	}
	registered := map[string]bool{}
	for _, p := range registeredAPIPaths(t) {
		registered[p] = true
	}
	for _, rt := range adminRoutes {
		if !registered[rt.path] {
			t.Errorf("%s is listed as an admin route and is not registered", rt.path)
		}
	}
}

// registeredAPIPaths reads the /api paths out of the route table's source, so
// the guard test above cannot fall behind the table.
func registeredAPIPaths(t *testing.T) []string {
	t.Helper()
	mux := newServer().routes()
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(routeSource(t), "\n") {
		i := strings.Index(line, `mux.HandleFunc("/api`)
		if i < 0 {
			continue
		}
		rest := line[i+len(`mux.HandleFunc("`):]
		path := rest[:strings.IndexByte(rest, '"')]
		if seen[path] {
			continue
		}
		seen[path] = true
		// Registered for real, not just written in a comment.
		if _, pattern := mux.Handler(httptest.NewRequest("GET", path, nil)); pattern != path {
			t.Fatalf("%s is in the source but the mux resolves it to %q", path, pattern)
		}
		out = append(out, path)
	}
	if len(out) < 30 {
		t.Fatalf("found only %d /api routes — the source reader is broken", len(out))
	}
	return out
}

// routeSource is the body of routes() in server.go.
func routeSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	start := strings.Index(src, "func (srv *server) routes() *http.ServeMux {")
	if start < 0 {
		t.Fatal("routes() is not in server.go")
	}
	end := strings.Index(src[start:], "\n}\n")
	return src[start : start+end]
}

// A line written by a browser session names which one, by the id the signed-in
// devices list shows. "the panel" alone could not be traced to a device, so it
// could not be acted on.
func TestASessionActionNamesTheSession(t *testing.T) {
	isolateAccess(t)
	isolateAlerts(t)
	s := newServer()
	tok := s.sessions.create("203.0.113.9")
	r := httptest.NewRequest("POST", "/api/autobackup", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	var ran bool
	s.guard(ScopeWrite, ok(&ran))(httptest.NewRecorder(), r)
	if !ran {
		t.Fatal("a valid session was refused")
	}
	got := Audit(1)
	if len(got) != 1 || !strings.Contains(got[0].Who, sessionID(tok)) {
		t.Fatalf("recorded %+v, want the session id %s in it", got, sessionID(tok))
	}
}
