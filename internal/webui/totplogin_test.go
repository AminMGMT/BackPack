package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The login, end to end, with and without a second factor.
//
// This is the most security-relevant path in the package and until the config
// file could be redirected there was no way to exercise it: every assertion
// needed a password to exist, and the password lives under /etc. It does now —
// see configOverride — so these are about behaviour rather than about units.

// useConfigFile points the panel's configuration at a file this test owns.
func useConfigFile(t *testing.T, c Config) {
	t.Helper()
	prev := configOverride
	configOverride = filepath.Join(t.TempDir(), "webui.json")
	t.Cleanup(func() { configOverride = prev })
	if err := Save(c); err != nil {
		t.Fatalf("writing the test configuration: %v", err)
	}
}

// loginServer is a panel with nothing in it but the parts a login needs.
func loginServer() *server {
	return &server{sessions: newSessionStore(), pending: newPendingStore()}
}

func postLogin(t *testing.T, s *server, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "203.0.113.7:4444"
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.handleLogin(w, r)
	return w
}

func cookieNamed(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range (&http.Response{Header: w.Header()}).Cookies() {
		if c.Name == name && c.Value != "" {
			return c
		}
	}
	return nil
}

// With no second factor the password is still the whole login.
func TestWithoutASecondFactorThePasswordSignsYouIn(t *testing.T) {
	useConfigFile(t, Config{Password: "correct-horse"})
	limiter.reset("203.0.113.7")
	s := loginServer()

	w := postLogin(t, s, url.Values{"password": {"correct-horse"}})

	if w.Code != http.StatusSeeOther {
		t.Fatalf("login returned %d, want a redirect into the panel", w.Code)
	}
	if cookieNamed(w, sessionCookie) == nil {
		t.Fatal("no session cookie was set")
	}
}

// With one, the password buys the code prompt and nothing else. This is the
// whole claim the feature makes.
func TestWithASecondFactorThePasswordAloneIsNotEnough(t *testing.T) {
	secret, _ := newTOTPSecret()
	useConfigFile(t, Config{Password: "correct-horse", TOTPSecret: secret})
	limiter.reset("203.0.113.7")
	s := loginServer()

	w := postLogin(t, s, url.Values{"password": {"correct-horse"}})

	if cookieNamed(w, sessionCookie) != nil {
		t.Fatal("the password alone produced a session on a panel with two-factor on")
	}
	pending := cookieNamed(w, twoFactorCookie)
	if pending == nil {
		t.Fatal("the password did not open the code prompt either")
	}
	if !strings.Contains(w.Body.String(), "authenticator") {
		t.Errorf("the page returned is not the code prompt:\n%s", w.Body.String())
	}

	// And then the code completes it.
	code, _ := totpCode(secret, time.Now())
	w2 := postLogin(t, s, url.Values{"code": {code}}, pending)
	if w2.Code != http.StatusSeeOther {
		t.Fatalf("the code step returned %d, want a redirect into the panel", w2.Code)
	}
	if cookieNamed(w2, sessionCookie) == nil {
		t.Fatal("a correct code did not produce a session")
	}
}

// A code with no pending token behind it is a password attempt with no
// password — it must not be treated as a second step.
func TestACodeWithoutTheFirstStepIsNotALogin(t *testing.T) {
	secret, _ := newTOTPSecret()
	useConfigFile(t, Config{Password: "correct-horse", TOTPSecret: secret})
	limiter.reset("203.0.113.7")
	s := loginServer()

	code, _ := totpCode(secret, time.Now())
	w := postLogin(t, s, url.Values{"code": {code}})

	if cookieNamed(w, sessionCookie) != nil {
		t.Fatal("a code alone signed somebody in")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("returned %d, want 401", w.Code)
	}
}

// The pending token is bound to the address that earned it. Without that it
// would be a three-minute password bypass for anybody who could read a cookie.
func TestAPendingTokenIsUselessFromAnotherAddress(t *testing.T) {
	secret, _ := newTOTPSecret()
	useConfigFile(t, Config{Password: "correct-horse", TOTPSecret: secret})
	limiter.reset("203.0.113.7")
	limiter.reset("198.51.100.9")
	s := loginServer()

	pending := cookieNamed(postLogin(t, s, url.Values{"password": {"correct-horse"}}), twoFactorCookie)
	if pending == nil {
		t.Fatal("no pending token to test with")
	}

	code, _ := totpCode(secret, time.Now())
	r := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{"code": {code}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "198.51.100.9:5555" // somewhere else
	r.AddCookie(pending)
	w := httptest.NewRecorder()
	s.handleLogin(w, r)

	if cookieNamed(w, sessionCookie) != nil {
		t.Fatal("a pending token was accepted from an address that did not earn it")
	}
}

// A recovery code is accepted at the prompt and then is spent.
func TestARecoveryCodeSignsYouInOnceAndThenDoesNot(t *testing.T) {
	secret, _ := newTOTPSecret()
	codes, hashes, _ := newRecoveryCodes()
	useConfigFile(t, Config{Password: "correct-horse", TOTPSecret: secret, RecoveryHashes: hashes})
	limiter.reset("203.0.113.7")
	s := loginServer()

	pending := cookieNamed(postLogin(t, s, url.Values{"password": {"correct-horse"}}), twoFactorCookie)
	w := postLogin(t, s, url.Values{"code": {codes[0]}}, pending)
	if cookieNamed(w, sessionCookie) == nil {
		t.Fatalf("a recovery code was refused at the prompt (status %d)", w.Code)
	}
	if left := len(Load().RecoveryHashes); left != recoveryCodeCount-1 {
		t.Fatalf("%d recovery codes left after using one, want %d", left, recoveryCodeCount-1)
	}

	// The same code again, on a fresh prompt.
	limiter.reset("203.0.113.7")
	pending2 := cookieNamed(postLogin(t, s, url.Values{"password": {"correct-horse"}}), twoFactorCookie)
	w2 := postLogin(t, s, url.Values{"code": {codes[0]}}, pending2)
	if cookieNamed(w2, sessionCookie) != nil {
		t.Fatal("a spent recovery code worked a second time")
	}
}

// Turning the second factor off needs the password again, because a stolen
// session must not be able to remove the thing that would have stopped it.
func TestTurningTheSecondFactorOffNeedsThePassword(t *testing.T) {
	secret, _ := newTOTPSecret()
	useConfigFile(t, Config{Password: "correct-horse", TOTPSecret: secret})
	s := loginServer()

	post := func(form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/totp", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.handleTOTP(w, r)
		return w
	}

	if w := post(url.Values{"action": {"disable"}, "password": {"guessing"}}); w.Code != http.StatusForbidden {
		t.Fatalf("disabling with the wrong password returned %d, want 403", w.Code)
	}
	if Load().TOTPSecret == "" {
		t.Fatal("the second factor was removed by a wrong password")
	}

	if w := post(url.Values{"action": {"disable"}, "password": {"correct-horse"}}); w.Code != http.StatusOK {
		t.Fatalf("disabling with the right password returned %d: %s", w.Code, w.Body)
	}
	if c := Load(); c.TOTPSecret != "" || len(c.RecoveryHashes) != 0 {
		t.Fatal("disabling left the secret or the recovery codes behind")
	}
}

// Enrolment does not take effect until a code proves the app holds the secret.
// Otherwise an operator who closes the tab after the QR code is a locked-out
// operator.
func TestEnrolmentNeedsACodeBeforeItCounts(t *testing.T) {
	useConfigFile(t, Config{Password: "correct-horse"})
	s := loginServer()

	post := func(form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/totp", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.handleTOTP(w, r)
		return w
	}

	started := post(url.Values{"action": {"start"}})
	if started.Code != http.StatusOK {
		t.Fatalf("starting enrolment returned %d", started.Code)
	}
	if Load().TOTPSecret != "" {
		t.Fatal("starting enrolment turned the second factor on before anything was proved")
	}
	if !strings.Contains(started.Body.String(), "otpauth://totp/") {
		t.Fatalf("no provisioning URI to scan:\n%s", started.Body)
	}

	if w := post(url.Values{"action": {"confirm"}, "code": {"000000"}}); w.Code != http.StatusBadRequest {
		t.Fatalf("a wrong confirmation code returned %d, want 400", w.Code)
	}
	if Load().TOTPSecret != "" {
		t.Fatal("a wrong code enabled the second factor")
	}

	s.enrolMu.Lock()
	secret := s.enrolling
	s.enrolMu.Unlock()
	code, _ := totpCode(secret, time.Now())

	w := post(url.Values{"action": {"confirm"}, "code": {code}})
	if w.Code != http.StatusOK {
		t.Fatalf("confirming returned %d: %s", w.Code, w.Body)
	}
	if Load().TOTPSecret != secret {
		t.Fatal("confirming did not store the secret")
	}
	// Recovery codes come with it, shown once. A second factor with no way back
	// is a way to lose a server.
	if !strings.Contains(w.Body.String(), "recovery") {
		t.Fatalf("no recovery codes were issued with the secret:\n%s", w.Body)
	}
	if len(Load().RecoveryHashes) != recoveryCodeCount {
		t.Fatalf("%d recovery hashes stored, want %d", len(Load().RecoveryHashes), recoveryCodeCount)
	}
}
