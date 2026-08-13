package webui

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Five failures lock the address out; a success wipes the slate.
func TestLoginLimiter(t *testing.T) {
	l := newLoginLimiter()
	ip := "203.0.113.9"

	for i := 0; i < loginMaxFails-1; i++ {
		l.fail(ip)
		if b, _ := l.blocked(ip); b {
			t.Fatalf("blocked after %d failures", i+1)
		}
	}
	l.fail(ip)
	if b, _ := l.blocked(ip); !b {
		t.Fatal("not blocked after reaching the limit")
	}

	l.reset(ip)
	if b, _ := l.blocked(ip); b {
		t.Fatal("still blocked after reset")
	}
}

// The limiter keys on the source address, and the source address is chosen by
// whoever is connecting. Remembering every one of them forever turned a
// defence against brute force into a way to grow the panel's memory from
// outside it.
func TestLoginLimiterStaysBounded(t *testing.T) {
	l := newLoginLimiter()
	for i := 0; i < loginMaxTracked*2; i++ {
		l.fail(fmt.Sprintf("198.51.100.%d:%d", i%256, i))
	}
	if len(l.byIP) > loginMaxTracked {
		t.Fatalf("the limiter is tracking %d addresses, want at most %d", len(l.byIP), loginMaxTracked)
	}
}

// Evicting must not let a blocked address back in early — the one being kept
// out is exactly the one still knocking.
func TestLoginLimiterKeepsBlockingUnderAddressChurn(t *testing.T) {
	l := newLoginLimiter()
	attacker := "203.0.113.7"
	for i := 0; i < loginMaxFails; i++ {
		l.fail(attacker)
	}
	if blocked, _ := l.blocked(attacker); !blocked {
		t.Fatal("the attacker was not blocked to begin with")
	}

	for i := 0; i < loginMaxTracked*2; i++ {
		l.fail(fmt.Sprintf("198.51.100.%d:%d", i%256, i))
		l.blocked(attacker) // as a real request would, on every attempt
	}

	if blocked, _ := l.blocked(attacker); !blocked {
		t.Fatal("a flood of other addresses let the blocked one back in")
	}
}

// The Secure flag is the one cookie attribute that can lock an operator out: a
// browser will not send a Secure cookie over plain HTTP, so a panel served on
// HTTP that sets it would log you in and then treat you as a stranger.
func TestAuthCookieIsOnlySecureOverTLS(t *testing.T) {
	plain := httptest.NewRequest(http.MethodPost, "http://panel.example/login", nil)
	if c := authCookie(plain, sessionCookie, "tok", sessionTTL); c.Secure {
		t.Error("a cookie set over plain HTTP was marked Secure; the panel would be unusable")
	}

	overTLS := httptest.NewRequest(http.MethodPost, "https://panel.example/login", nil)
	overTLS.TLS = &tls.ConnectionState{}
	if c := authCookie(overTLS, sessionCookie, "tok", sessionTTL); !c.Secure {
		t.Error("a cookie set over TLS was not marked Secure")
	}
}

// A forwarded-protocol header is not evidence: anyone can send it, and
// believing it on a plain-HTTP panel would lock out its owner.
func TestForwardedProtoDoesNotMakeACookieSecure(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://panel.example/login", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	if c := authCookie(r, sessionCookie, "tok", sessionTTL); c.Secure {
		t.Error("a spoofable header was enough to set Secure")
	}
}

// Clearing a cookie only works if the attributes match the one being replaced.
func TestClearedCookieMatchesTheCookieItReplaces(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://panel.example/logout", nil)
	set := authCookie(r, sessionCookie, "tok", sessionTTL)
	cleared := clearedCookie(r, sessionCookie)

	if cleared.Path != set.Path || cleared.Secure != set.Secure ||
		cleared.HttpOnly != set.HttpOnly || cleared.SameSite != set.SameSite {
		t.Fatalf("cleared cookie %+v does not match the one it replaces %+v", cleared, set)
	}
	if cleared.MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want it negative so the browser drops the cookie", cleared.MaxAge)
	}
}

// Every cookie the panel sets must be HttpOnly and SameSite — a session token
// readable from script is a session token an injected script can take.
func TestPanelCookiesAreHttpOnlyAndSameSite(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://panel.example/login", nil)
	for _, name := range []string{sessionCookie, pendingCookie} {
		c := authCookie(r, name, "value", time.Minute)
		if !c.HttpOnly {
			t.Errorf("%s is readable from script", name)
		}
		if c.SameSite != http.SameSiteLaxMode {
			t.Errorf("%s SameSite = %v, want Lax", name, c.SameSite)
		}
		if c.Path != "/" {
			t.Errorf("%s Path = %q, want /", name, c.Path)
		}
	}
}

func TestPanelSecurityHeaders(t *testing.T) {
	handler := withPanelSecurity(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://panel.example/", nil))

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for header, value := range want {
		if got := w.Header().Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy = %q, want it to forbid framing", csp)
	}
}

func TestPanelServerHasItsLimits(t *testing.T) {
	srv := &http.Server{}
	panelServerLimits(srv)
	if srv.ReadHeaderTimeout == 0 || srv.IdleTimeout == 0 || srv.MaxHeaderBytes == 0 {
		t.Fatalf("the panel server was built without its limits: %+v", srv)
	}
}

// A code works exactly once; three wrong tries kill the pending login.
func TestTwoFAStore(t *testing.T) {
	st := &twoFAStore{pending: map[string]*pendingLogin{}}

	tok, code := st.start()
	if ok, _ := st.verify(tok, code); !ok {
		t.Fatal("correct code rejected")
	}
	if ok, dead := st.verify(tok, code); ok || !dead {
		t.Fatal("a code must not work twice")
	}

	tok, _ = st.start()
	for i := 0; i < twoFAMaxAttempts-1; i++ {
		if _, dead := st.verify(tok, "000000"); dead {
			t.Fatalf("killed after %d attempts", i+1)
		}
	}
	if _, dead := st.verify(tok, "000000"); !dead {
		t.Fatal("pending login should die after max attempts")
	}
}
