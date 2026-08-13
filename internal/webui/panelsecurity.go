package webui

import (
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
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; "+
				"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
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
