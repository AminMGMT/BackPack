package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolateAccess points the token and audit files at a temp directory, so a test
// never reads or writes the machine it runs on.
func isolateAccess(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	oldTokens, oldAudit := TokensPath, AuditPath
	TokensPath = filepath.Join(dir, "api-tokens.json")
	AuditPath = filepath.Join(dir, "audit.json")
	t.Cleanup(func() { TokensPath, AuditPath = oldTokens, oldAudit })
}

// ok is a handler that records it ran.
func ok(ran *bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*ran = true
		w.WriteHeader(http.StatusOK)
	}
}

func req(method, path, token string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestAnUnauthenticatedAPICallIsRefused(t *testing.T) {
	isolateAccess(t)
	s := &server{sessions: newSessionStore()}
	var ran bool
	w := httptest.NewRecorder()
	s.guard(ScopeRead, ok(&ran))(w, req("GET", "/api/stats", ""))
	if ran {
		t.Fatal("the handler ran for a request with no credential")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// The reason tokens came back: a scraper has no cookie, and /metrics is an
// endpoint built for scrapers that a cookie check had put out of reach.
func TestAReadTokenReachesMetrics(t *testing.T) {
	isolateAccess(t)
	secret, _, err := IssueToken("prometheus", ScopeRead, time.Hour)
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	s := &server{sessions: newSessionStore()}
	var ran bool
	w := httptest.NewRecorder()
	s.guard(ScopeRead, ok(&ran))(w, req("GET", "/metrics", secret))
	if !ran {
		t.Fatalf("a read token was refused from /metrics: %d", w.Code)
	}
}

// Scope is the whole point. A token for a scraper must not be able to restart
// anything.
func TestAReadTokenCannotWrite(t *testing.T) {
	isolateAccess(t)
	secret, _, err := IssueToken("prometheus", ScopeRead, time.Hour)
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	s := &server{sessions: newSessionStore()}
	var ran bool
	w := httptest.NewRecorder()
	s.guard(ScopeWrite, ok(&ran))(w, req("POST", "/api/nodes", secret))
	if ran {
		t.Fatal("a read-only token performed a write")
	}
	// 403, not 401: it proved who it is and is not allowed to do this, which
	// is a different answer and a more useful one to debug against.
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

// A write token must not be able to mint itself a better one.
func TestAWriteTokenCannotReachTheAdminEndpoints(t *testing.T) {
	isolateAccess(t)
	secret, _, err := IssueToken("ci", ScopeWrite, time.Hour)
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	s := &server{sessions: newSessionStore()}
	var ran bool
	w := httptest.NewRecorder()
	s.guard(ScopeAdmin, ok(&ran))(w, req("POST", "/api/tokens", secret))
	if ran {
		t.Fatal("a write token reached the token endpoint")
	}
}

func TestAnExpiredTokenIsRefused(t *testing.T) {
	isolateAccess(t)
	secret, _, err := IssueToken("old", ScopeRead, time.Millisecond)
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, ok := checkToken(secret); ok {
		t.Fatal("an expired token was accepted")
	}
}

func TestARevokedTokenIsRefused(t *testing.T) {
	isolateAccess(t)
	secret, _, err := IssueToken("temp", ScopeWrite, time.Hour)
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	if _, ok := checkToken(secret); !ok {
		t.Fatal("setup: the token did not work")
	}
	if err := RevokeToken("temp"); err != nil {
		t.Fatalf("revoking: %v", err)
	}
	if _, ok := checkToken(secret); ok {
		t.Fatal("a revoked token still worked")
	}
}

// A token that can be read back out of the panel is a token that leaks with a
// backup. Only the hash is stored.
func TestTheSecretIsNeverStored(t *testing.T) {
	isolateAccess(t)
	secret, _, err := IssueToken("leaky", ScopeRead, time.Hour)
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	data, err := os.ReadFile(TokensPath)
	if err != nil {
		t.Fatalf("reading the store: %v", err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("the secret itself is on disk")
	}
	for _, v := range ListTokens() {
		if strings.Contains(v.Hash, secret) {
			t.Fatal("the secret leaked into the listing")
		}
	}
}

// A credential file readable by anything else on the machine is not a
// credential.
func TestTheTokenFileIsNotWorldReadable(t *testing.T) {
	isolateAccess(t)
	if _, _, err := IssueToken("x", ScopeRead, time.Hour); err != nil {
		t.Fatalf("issuing: %v", err)
	}
	info, err := os.Stat(TokensPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("mode = %v, want nothing for group or other", mode)
	}
}

// Both of these exist because the previous token was removed for being
// unrotatable: nothing issued it and nothing could tell whether it was dead.
func TestATokenNeedsANameAndAnExpiry(t *testing.T) {
	isolateAccess(t)
	if _, _, err := IssueToken("  ", ScopeRead, time.Hour); err == nil {
		t.Fatal("an unnamed token was issued")
	}
	if _, _, err := IssueToken("forever", ScopeRead, 0); err == nil {
		t.Fatal("a token with no expiry was issued")
	}
}

func TestTokenNamesAreUnique(t *testing.T) {
	isolateAccess(t)
	if _, _, err := IssueToken("dup", ScopeRead, time.Hour); err != nil {
		t.Fatalf("issuing: %v", err)
	}
	if _, _, err := IssueToken("dup", ScopeWrite, time.Hour); err == nil {
		t.Fatal("two tokens were issued with the same name — revoking one becomes ambiguous")
	}
}

// The record is written by the guard, so an action cannot be authorised
// without being recorded.
func TestAWriteIsRecorded(t *testing.T) {
	isolateAccess(t)
	secret, _, err := IssueToken("ci", ScopeWrite, time.Hour)
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	s := &server{sessions: newSessionStore()}
	var ran bool
	r := req("POST", "/api/nodes?action=upgradeall", secret)
	s.guard(ScopeWrite, ok(&ran))(httptest.NewRecorder(), r)
	if !ran {
		t.Fatal("setup: the handler did not run")
	}

	entries := Audit(10)
	if len(entries) != 1 {
		t.Fatalf("recorded %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Method != "POST" || e.Path != "/api/nodes" {
		t.Fatalf("entry = %+v", e)
	}
	// The panel routes a dozen fleet operations through one path; recording
	// them all as "POST /api/nodes" would be recording nothing.
	if e.Action != "upgradeall" {
		t.Fatalf("action = %q, want the sub-operation", e.Action)
	}
	if !strings.Contains(e.Who, "ci") {
		t.Fatalf("who = %q, want the token's name", e.Who)
	}
}

// A refused attempt is often the more interesting line.
func TestARefusedWriteIsRecorded(t *testing.T) {
	isolateAccess(t)
	secret, _, err := IssueToken("scraper", ScopeRead, time.Hour)
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	s := &server{sessions: newSessionStore()}
	var ran bool
	s.guard(ScopeWrite, ok(&ran))(httptest.NewRecorder(), req("POST", "/api/nodes", secret))

	entries := Audit(10)
	if len(entries) != 1 {
		t.Fatalf("a refused attempt was not recorded: %+v", entries)
	}
	if entries[0].Status != http.StatusForbidden {
		t.Fatalf("status = %d, want the refusal", entries[0].Status)
	}
}

// A panel being polled every few seconds would bury the lines that matter
// under thousands that do not, and a record nobody can read is not a record.
func TestReadsAreNotRecorded(t *testing.T) {
	isolateAccess(t)
	secret, _, err := IssueToken("scraper", ScopeRead, time.Hour)
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	s := &server{sessions: newSessionStore()}
	var ran bool
	for i := 0; i < 5; i++ {
		s.guard(ScopeRead, ok(&ran))(httptest.NewRecorder(), req("GET", "/metrics", secret))
	}
	if n := len(Audit(10)); n != 0 {
		t.Fatalf("recorded %d reads", n)
	}
}

// An audit file that cannot be written must not be a reason the panel stops
// working: a full disk would otherwise lock the operator out of the tool they
// need to fix it.
func TestAnUnwritableRecordDoesNotBlockTheAction(t *testing.T) {
	isolateAccess(t)
	// A directory where the file should be: every write fails.
	if err := os.Mkdir(AuditPath, 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	secret, _, err := IssueToken("ci", ScopeWrite, time.Hour)
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	s := &server{sessions: newSessionStore()}
	var ran bool
	s.guard(ScopeWrite, ok(&ran))(httptest.NewRecorder(), req("POST", "/api/nodes", secret))
	if !ran {
		t.Fatal("an unwritable audit file stopped the action")
	}
}

// The record is capped, or a long-lived panel grows a file nobody can open.
func TestTheRecordIsBounded(t *testing.T) {
	isolateAccess(t)
	old := auditKeep
	auditKeep = 25
	t.Cleanup(func() { auditKeep = old })
	for i := 0; i < auditKeep+50; i++ {
		record(auditEntry{At: time.Now().Unix(), Who: "test", Method: "POST", Path: "/x"})
	}
	if n := len(Audit(0)); n > auditKeep {
		t.Fatalf("the record holds %d entries, cap is %d", n, auditKeep)
	}
}

// The newest entries are what an investigation starts from.
func TestTheRecordReadsNewestFirst(t *testing.T) {
	isolateAccess(t)
	record(auditEntry{At: 1, Who: "first", Method: "POST", Path: "/a"})
	record(auditEntry{At: 2, Who: "second", Method: "POST", Path: "/b"})
	got := Audit(10)
	if len(got) != 2 || got[0].Who != "second" {
		t.Fatalf("order = %+v", got)
	}
}

func TestScopeParsing(t *testing.T) {
	for text, want := range map[string]Scope{
		"read": ScopeRead, "ro": ScopeRead, "read-only": ScopeRead,
		"write": ScopeWrite, "rw": ScopeWrite,
		"admin": ScopeAdmin, "owner": ScopeAdmin,
	} {
		got, err := ParseScope(text)
		if err != nil || got != want {
			t.Fatalf("ParseScope(%q) = %v, %v", text, got, err)
		}
	}
	if _, err := ParseScope("superuser"); err == nil {
		t.Fatal("an unknown scope was accepted")
	}
}

// The endpoint has to hand the secret back exactly once, because there is no
// second chance to read it.
func TestIssuingOverTheAPIReturnsTheSecretOnce(t *testing.T) {
	isolateAccess(t)
	s := &server{sessions: newSessionStore()}

	r := httptest.NewRequest("POST", "/api/tokens",
		strings.NewReader("name=grafana&scope=read&days=30"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleTokens(w, r)

	var body struct {
		Secret string      `json:"secret"`
		Tokens []tokenView `json:"tokens"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v (%s)", err, w.Body)
	}
	if body.Secret == "" {
		t.Fatal("no secret came back from issuing a token")
	}
	if len(body.Tokens) != 1 || body.Tokens[0].Name != "grafana" {
		t.Fatalf("listing = %+v", body.Tokens)
	}
	if !body.Tokens[0].Unused {
		t.Fatal("a brand new token is not marked unused")
	}

	// And the listing never carries it again.
	w2 := httptest.NewRecorder()
	s.handleTokens(w2, httptest.NewRequest("GET", "/api/tokens", nil))
	if strings.Contains(w2.Body.String(), body.Secret) {
		t.Fatal("the listing handed the secret back")
	}
}
