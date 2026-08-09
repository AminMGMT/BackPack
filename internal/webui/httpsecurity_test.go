package webui

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthCookiesUseStrictProductionAttributes(t *testing.T) {
	w := httptest.NewRecorder()
	http.SetCookie(w, newAuthCookie("session", "token", time.Hour, true))
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	c := cookies[0]
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("insecure session cookie: Secure=%v HttpOnly=%v SameSite=%v", c.Secure, c.HttpOnly, c.SameSite)
	}
	if c.MaxAge <= 0 || c.Expires.Before(time.Now()) {
		t.Fatal("session cookie has no usable expiry")
	}

	w = httptest.NewRecorder()
	http.SetCookie(w, expiredAuthCookie("session", true))
	c = w.Result().Cookies()[0]
	if c.MaxAge != -1 || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie deletion did not preserve security attributes: %+v", c)
	}
}

func TestSecureHTTPHeadersAndOriginChecks(t *testing.T) {
	called := 0
	h := securePanelHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusNoContent)
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://panel.example/", nil))
	if w.Code != http.StatusNoContent || called != 1 {
		t.Fatalf("same-origin GET status=%d called=%d", w.Code, called)
	}
	for _, name := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Permissions-Policy"} {
		if w.Header().Get(name) == "" {
			t.Errorf("missing security header %s", name)
		}
	}

	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://panel.example/api/password", nil)
	req.Header.Set("Origin", "https://evil.example")
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || called != 1 {
		t.Fatalf("cross-origin POST status=%d called=%d", w.Code, called)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "https://panel.example/api/password", nil)
	req.TLS = &tls.ConnectionState{}
	req.Header.Set("Origin", "https://panel.example")
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent || called != 2 {
		t.Fatalf("same-origin HTTPS POST status=%d called=%d", w.Code, called)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "http://panel.example/api/password", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || called != 2 {
		t.Fatalf("cross-site fetch metadata status=%d called=%d", w.Code, called)
	}
}

func TestAuthFormSizeAndPasswordComparison(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password="+strings.Repeat("x", maxAuthForm)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if parseAuthForm(w, req) || w.Code != http.StatusBadRequest {
		t.Fatalf("oversized login form accepted with status %d", w.Code)
	}
	if !passwordMatches("correct horse", "correct horse") || passwordMatches("wrong", "correct horse") {
		t.Fatal("fixed-length password comparison returned the wrong result")
	}
}

func TestPanelHTTPServerTimeouts(t *testing.T) {
	srv := newPanelHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout != 10*time.Second || srv.ReadTimeout != 15*time.Second ||
		srv.WriteTimeout != 30*time.Second || srv.IdleTimeout != 60*time.Second || srv.MaxHeaderBytes != 32<<10 {
		t.Fatalf("unexpected panel HTTP limits: %+v", srv)
	}
}
