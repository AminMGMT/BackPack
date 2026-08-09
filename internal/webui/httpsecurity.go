package webui

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxAuthForm = 8 << 10

func parseAuthForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthForm)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid or oversized form", http.StatusBadRequest)
		return false
	}
	return true
}

func passwordMatches(given, want string) bool {
	gotHash := sha256.Sum256([]byte(given))
	wantHash := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gotHash[:], wantHash[:]) == 1
}

func newAuthCookie(name, value string, ttl time.Duration, secure bool) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: value, Path: "/",
		HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(ttl / time.Second), Expires: time.Now().Add(ttl),
	}
}

func expiredAuthCookie(name string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: "", Path: "/",
		HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1, Expires: time.Unix(1, 0),
	}
}

func securePanelHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setPanelSecurityHeaders(w)
		if isUnsafeMethod(r.Method) && !sameOriginRequest(r) {
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func setPanelSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' https://api.github.com; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'; worker-src 'self'; manifest-src 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
}

func isUnsafeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func sameOriginRequest(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser clients; SameSite protects browser cookies
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return strings.EqualFold(u.Scheme, scheme)
}

func newPanelHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
}
