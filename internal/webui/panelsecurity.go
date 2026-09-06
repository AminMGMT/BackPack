package webui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"time"
)

// maxLoginBody bounds the login form. ParseForm reads the whole body into
// memory before anything looks at it, and the one endpoint that runs before
// any authentication is the one where that matters: two fields of a few dozen
// bytes had no reason to accept a request of any size at all.
const maxLoginBody = 8 << 10

// secureRequest reports whether this request reached the panel over TLS.
//
// The Secure attribute is the one cookie flag that can lock an operator out:
// a browser will not send a Secure cookie over plain HTTP, so setting it on a
// panel served over HTTP means logging in appears to work and every request
// after it is unauthenticated. It is therefore set only on direct evidence of
// TLS — this connection, or a panel configured to serve HTTPS itself.
//
// X-Forwarded-Proto is deliberately not consulted. Behind a TLS-terminating
// proxy the header would be right, but any client can send it, and a panel on
// plain HTTP that believed it would lock out the very person it belongs to.
// Not setting Secure behind such a proxy costs nothing the proxy has not
// already decided; setting it wrongly costs the panel.
func secureRequest(r *http.Request) bool {
	return r.TLS != nil || Load().HTTPS
}

// authCookie builds a panel cookie with the attributes every one of them
// should have. They were set in three places with three slightly different
// sets of flags; one constructor is how they stay in agreement.
func authCookie(r *http.Request, name, value string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		// Lax rather than Strict on purpose: Strict withholds the cookie on a
		// top-level navigation that came from anywhere else, so following a
		// link to the panel — from the Telegram bot's message, say — would
		// land on the login page despite a live session. Lax already keeps the
		// cookie off cross-site POSTs, which is the case that matters.
		SameSite: http.SameSiteLaxMode,
		Secure:   secureRequest(r),
		MaxAge:   int(ttl / time.Second),
	}
}

// clearedCookie expires one. The attributes have to match the cookie being
// replaced or the browser keeps the original alongside it.
func clearedCookie(r *http.Request, name string) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secureRequest(r),
		MaxAge:   -1,
	}
}

// withPanelSecurity adds the response headers the panel was serving without.
//
// The panel drives tunnels — it creates them, edits them and restarts them —
// from a page that could be framed by any other site, on responses a browser
// was free to sniff a content type out of and to send on as a referrer to
// wherever a link led.
func withPanelSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// The pages are self-contained: no CDN, no external fonts, no remote
		// images. frame-ancestors is X-Frame-Options for browsers that have
		// moved on from it.
		//
		// script-src carries a per-response nonce instead of 'unsafe-inline'.
		//
		// 'unsafe-inline' is what makes an injected string executable: a name
		// that reaches the DOM carrying onerror= runs, and the policy that is
		// supposed to be the last line under a missed escape permits exactly
		// the thing the escape was there to stop. A nonce does not — it admits
		// the two script blocks this panel actually ships, which are handed the
		// value below, and nothing else. Attribute handlers are never covered
		// by a nonce, which is the point: there is no way to spell one that
		// this policy allows.
		//
		// The templates in panel/views carry inline onclick from the preview
		// they were drawn as, and none of it survives — screen.js rewrites the
		// calls into data-fn and strips every remaining on* attribute before
		// the markup reaches the document. See loadTemplate there.
		//
		// style-src keeps 'unsafe-inline': the panel sets element.style
		// throughout, and a style attribute is not script.
		nonce := newCSPNonce()
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'nonce-"+nonce+"'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; "+
				"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), cspNonceKey{}, nonce)))
	})
}

// panelServerLimits applies the timeouts and caps a listener on a public port
// needs. ReadTimeout and WriteTimeout were already set; what was missing is a
// separate deadline for the headers and a ceiling on how long an idle
// keep-alive connection may sit there holding a goroutine.
func panelServerLimits(srv *http.Server) {
	srv.ReadHeaderTimeout = 10 * time.Second
	srv.IdleTimeout = 90 * time.Second
	srv.MaxHeaderBytes = 32 << 10
}

// The per-response script nonce.
//
// Generated fresh for every request and passed down on the context, so the two
// pages that carry an inline script can stamp it into the tag they serve. A
// nonce that were reused across responses would be one an attacker could read
// from an earlier page and reuse, which is a nonce in name only.
type cspNonceKey struct{}

// newCSPNonce returns 16 random bytes, base64. crypto/rand, because the whole
// value of the nonce is that it cannot be guessed before the response carrying
// it is read.
func newCSPNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Unreachable in practice; crypto/rand does not fail on the platforms
		// this runs on. If it ever did, an empty nonce fails closed — the
		// inline script would not run and the panel would visibly not work,
		// which is the right way round for a security control.
		return ""
	}
	return base64.RawStdEncoding.EncodeToString(b[:])
}

// cspNonce is the nonce for this request, or "" outside the handler chain.
func cspNonce(r *http.Request) string {
	n, _ := r.Context().Value(cspNonceKey{}).(string)
	return n
}

// noncePlaceholder is what the shipped HTML carries where the nonce goes. The
// pages are embedded files, not templates: one replace at serve time keeps
// them readable on disk and editable without a build step.
const noncePlaceholder = "__CSP_NONCE__"

// withNonce stamps this request's nonce into a page.
func withNonce(page []byte, r *http.Request) []byte {
	return bytes.ReplaceAll(page, []byte(noncePlaceholder), []byte(cspNonce(r)))
}
