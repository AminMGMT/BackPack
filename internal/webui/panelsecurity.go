package webui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
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

// crossSiteNavigation reports whether a request arrived from somewhere other
// than this panel.
//
// Sec-Fetch-Site is what answers it. Every browser that matters sends it on
// every navigation: "same-origin" for a link inside the panel, "cross-site" for
// one on somebody else's page, and "none" for an address typed into the bar or
// opened from a bookmark. A request with no such header at all — an old
// browser, curl — is treated as same-site, because refusing it would break the
// ordinary case to guard against one this header exists to describe.
//
// It is used on /logout, which changes state on a GET. A logout is not a
// dangerous thing to be tricked into, and it is the one mutating route that has
// to stay a link: the panel's own sign-out is an <a href>. What it is is
// annoying, repeatedly, from any page an operator can be persuaded to open —
// and SameSite=Lax does not stop it, because Lax exists precisely to keep
// sending the cookie on a top-level navigation. Reaching it at all needs the
// panel's unguessable path, so this is closing the last step of an attack that
// has already got past the part that matters.
func crossSiteNavigation(r *http.Request) bool {
	return r.Header.Get("Sec-Fetch-Site") == "cross-site"
}

// withPanelSecurity adds the response headers the panel was serving without.
//
// The panel drives tunnels — it creates them, edits them and restarts them —
// from a page that could be framed by any other site, on responses a browser
// was free to sniff a content type out of and to send on as a referrer to
// wherever a link led.
func withPanelSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A request that changes something has to have come from this panel.
		//
		// What stops cross-site requests today is a chain of three things, each
		// of which holds: the session cookie is SameSite=Lax, so it is not sent
		// on a cross-site POST; every mutating handler enforces POST; and the
		// panel answers only under a path nobody can guess. That is enough, and
		// it means the base path is a load-bearing control rather than the
		// obscurity it is described as — a chain where the first link is a
		// cookie attribute and the last is a secret in the address bar.
		//
		// This is a fourth, and the only one that is about the question being
		// asked. A browser says where a request came from; an attacker's page
		// cannot make it say something else. Absent — curl, a script, a peer
		// panel holding the remote access token — is treated as same-site,
		// because refusing those would break every caller that is not a browser
		// to guard against one only a browser can perform.
		if r.Method != http.MethodGet && r.Method != http.MethodHead &&
			r.Method != http.MethodOptions && crossSiteNavigation(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}

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

// The panel's base path.
//
// Everything the panel serves lives under one unguessable segment, so the panel
// answers at http://host:7777/x7Kq2p9wRt4mNs/ and at nothing else. Anything
// outside it gets a 404 — not a redirect, which would hand the path back to
// whoever asked.
//
// This is not authentication and is not a substitute for the password. It
// changes who ever reaches the password prompt. A panel on a known port at "/"
// is found by the sweeps within hours of being started and answers login
// attempts from strangers from then on; behind a path nobody can guess, those
// sweeps get a 404 and stop.
//
// It wraps the mux rather than being threaded through the routes, so every
// handler keeps the path it already had and nothing inside has to know.

// withBasePath serves next under prefix, and serves nothing anywhere else.
func withBasePath(prefix string, next http.Handler) http.Handler {
	if prefix == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == prefix:
			// The bare prefix is the panel's front door; the trailing slash is
			// what makes the page's relative assets resolve inside it.
			http.Redirect(w, r, prefix+"/", http.StatusMovedPermanently)
			return
		case strings.HasPrefix(r.URL.Path, prefix+"/"):
			// Compared before stripping, and stripped from both fields, so a
			// handler reading either sees the path it was written for.
			r2 := *r
			u := *r.URL
			u.Path = strings.TrimPrefix(r.URL.Path, prefix)
			if u.RawPath != "" {
				u.RawPath = strings.TrimPrefix(u.RawPath, prefix)
			}
			r2.URL = &u
			// Carried on the context so a handler that sends the browser
			// somewhere can put it back. Every handler below this sees a
			// stripped path, which is what lets the routes stay as they were —
			// and it means a Location built from one is an address outside the
			// panel. See redirectTo.
			next.ServeHTTP(w, r2.WithContext(
				context.WithValue(r.Context(), basePathKey{}, prefix)))
			return
		}
		// Everything else. The wording is the stock one on purpose: a panel
		// that answered "wrong path" would confirm there is a panel here.
		http.NotFound(w, r)
	})
}

// basePlaceholder is what the shipped HTML carries where the base path goes,
// stamped in at serve time beside the CSP nonce.
const basePlaceholder = "__BASE_PATH__"

// withBase stamps the base path into a page. "" is a panel at the root, and
// leaves every URL in the page exactly as it was written.
func withBase(page []byte, prefix string) []byte {
	return bytes.ReplaceAll(page, []byte(basePlaceholder), []byte(prefix))
}

// loginStatePlaceholder marks where the sign-in pages learn that the last
// attempt was refused, so they can say so instead of silently reappearing.
const loginStatePlaceholder = "__LOGIN_STATE__"

// withLoginState stamps "wrong" into a sign-in page after a refused attempt,
// and nothing otherwise.
func withLoginState(page []byte, refused bool) []byte {
	state := ""
	if refused {
		state = "wrong"
	}
	return bytes.ReplaceAll(page, []byte(loginStatePlaceholder), []byte(state))
}

// The base path this request arrived under.
type basePathKey struct{}

// requestBase is the prefix the panel is being served under for this request,
// or "" at the root.
//
// Read from the request rather than from disk: it has to be the prefix this
// request actually came in under, not what the config says now. Those differ
// for exactly one request after the path is changed — the one that changed it.
func requestBase(r *http.Request) string {
	p, _ := r.Context().Value(basePathKey{}).(string)
	return p
}

// redirectTo sends the browser to a path inside the panel.
//
// Handlers below withBasePath see stripped paths, so they name routes the way
// they are registered: "/login", "/". A Location built from one of those is an
// address at the root of the origin, where the panel does not answer — so the
// browser follows it to a 404 and the panel looks like it will not open.
//
// That is what happened: sign-in bounced to /login, the login form posted to a
// path that was not there, and logging out landed nowhere. Every redirect the
// panel sends goes through here.
func redirectTo(w http.ResponseWriter, r *http.Request, path string, code int) {
	http.Redirect(w, r, requestBase(r)+path, code)
}
