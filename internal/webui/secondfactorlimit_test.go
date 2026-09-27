package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// Knowing the password must not buy unlimited guesses at the second factor.
//
// The password step used to clear the IP's failure count even when all it
// earned was the code prompt, and a wrong code did not cost the pending token
// anything. So password, four wrong codes, password, four more … never reached
// the lockout: a six-digit code with three valid values per window, guessed
// without limit, in parallel. The second factor exists for exactly the caller
// who already has the password.
func TestThePasswordStepDoesNotResetSecondFactorFailures(t *testing.T) {
	secret, _ := newTOTPSecret()
	useConfigFile(t, Config{Password: "correct-horse", TOTPSecret: secret})
	limiter.reset("203.0.113.7")
	t.Cleanup(func() { limiter.reset("203.0.113.7") })
	s := loginServer()

	// Rounds of (password, then wrong codes) — far more wrong codes in total
	// than the lockout allows.
	blocked := false
	for round := 0; round < 4 && !blocked; round++ {
		w := postLogin(t, s, url.Values{"password": {"correct-horse"}})
		if w.Code == http.StatusTooManyRequests {
			blocked = true
			break
		}
		pending := cookieNamed(w, twoFactorCookie)
		if pending == nil {
			t.Fatalf("round %d: the password did not open the code prompt (status %d)", round, w.Code)
		}
		for i := 0; i < loginMaxFails-1; i++ {
			w := postLogin(t, s, url.Values{"code": {"000000"}}, pending)
			if w.Code == http.StatusTooManyRequests {
				blocked = true
				break
			}
		}
	}
	if !blocked {
		t.Fatal("wrong second-factor codes never led to a lockout: re-entering the password cleared the count each time")
	}
}

// A pending second-factor token stops accepting codes after a few wrong ones,
// whatever the per-IP counter says — a limit that moves with the attacker's
// address is not the only one.
func TestAPendingTokenDiesAfterRepeatedWrongCodes(t *testing.T) {
	secret, _ := newTOTPSecret()
	useConfigFile(t, Config{Password: "correct-horse", TOTPSecret: secret})
	limiter.reset("203.0.113.7")
	t.Cleanup(func() { limiter.reset("203.0.113.7") })
	s := loginServer()

	w := postLogin(t, s, url.Values{"password": {"correct-horse"}})
	pending := cookieNamed(w, twoFactorCookie)
	if pending == nil {
		t.Fatal("no code prompt")
	}
	for i := 0; i < pendingMaxFails; i++ {
		postLogin(t, s, url.Values{"code": {"000000"}}, pending)
		limiter.reset("203.0.113.7") // as if each guess came from a fresh address
	}
	if s.pending.valid(pending.Value, "203.0.113.7") {
		t.Fatalf("the pending token still accepts codes after %d wrong ones", pendingMaxFails)
	}
}

// When a pending sign-in has ended, the page shown is the password page, not a
// code prompt a correct code could no longer satisfy.
func TestAnEndedSignInGoesBackToThePassword(t *testing.T) {
	secret, _ := newTOTPSecret()
	useConfigFile(t, Config{Password: "correct-horse", TOTPSecret: secret})
	limiter.reset("203.0.113.7")
	t.Cleanup(func() { limiter.reset("203.0.113.7") })
	s := loginServer()

	pending := cookieNamed(postLogin(t, s, url.Values{"password": {"correct-horse"}}), twoFactorCookie)
	if pending == nil {
		t.Fatal("no code prompt")
	}
	var last *httptest.ResponseRecorder
	for i := 0; i < pendingMaxFails; i++ {
		last = postLogin(t, s, url.Values{"code": {"000000"}}, pending)
	}
	if strings.Contains(last.Body.String(), "authenticator") {
		t.Fatal("the code prompt was shown for a sign-in that has already ended")
	}
}

// Parallel requests cannot all pass the check before any is counted: the
// check and the count are one step, for the address and for the sign-in.
func TestParallelAttemptsAreCountedBeforeTheyAreChecked(t *testing.T) {
	const burst = 50
	l := newLoginLimiter()
	p := newPendingStore()
	tok := p.create("198.51.100.9")

	var mu sync.Mutex
	passedIP, passedTok := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _ := l.attempt("198.51.100.9")
			okTok := p.attempt(tok, "198.51.100.9")
			mu.Lock()
			if ok {
				passedIP++
			}
			if okTok {
				passedTok++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if passedIP != loginMaxFails {
		t.Errorf("%d of %d parallel attempts from one address went ahead; the limit is %d", passedIP, burst, loginMaxFails)
	}
	if passedTok != pendingMaxFails {
		t.Errorf("%d of %d parallel codes were checked against one sign-in; the limit is %d", passedTok, burst, pendingMaxFails)
	}
}
