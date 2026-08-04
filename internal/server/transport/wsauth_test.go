package transport

import (
	"crypto/tls"
	"net/http"
	"testing"
)

// req builds a websocket-upgrade request carrying the given Authorization
// value, optionally over TLS.
func req(auth string, overTLS bool) *http.Request {
	r, _ := http.NewRequest("GET", "http://example/channel", nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Key", "x")
	r.Header.Set("Sec-WebSocket-Version", "13")
	if auth != "" {
		r.Header.Set("Authorization", "Bearer "+auth)
	}
	if overTLS {
		// A minimal completed TLS state is enough for r.TLS != nil; the proof
		// path needs keying material it cannot export from this, which is
		// exactly the "cannot export → reject" case, tested separately below.
		r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, HandshakeComplete: true}
	}
	return r
}

const wsToken = "a-real-looking-tunnel-token-0123456789"

// Over plain ws the raw token is the credential, whether or not simple auth is
// on — there is no TLS session to bind to either way.
func TestPlainWSAcceptsTheRawToken(t *testing.T) {
	for _, simple := range []bool{false, true} {
		if !authorizeWSRequest(req(wsToken, false), wsToken, simple) {
			t.Errorf("plain ws rejected the correct token (simpleAuth=%v)", simple)
		}
		if authorizeWSRequest(req("wrong", false), wsToken, simple) {
			t.Errorf("plain ws accepted a wrong token (simpleAuth=%v)", simple)
		}
		if authorizeWSRequest(req("", false), wsToken, simple) {
			t.Errorf("plain ws accepted an empty credential (simpleAuth=%v)", simple)
		}
	}
}

// This is the fix. With simple auth on, a wss request authorises on the raw
// token — which is what a client behind a TLS-terminating proxy can still send,
// and what the bound-proof path would reject.
func TestSimpleAuthAcceptsTheRawTokenOverTLS(t *testing.T) {
	if !authorizeWSRequest(req(wsToken, true), wsToken, true) {
		t.Fatal("simple auth rejected the raw token over TLS — the NGINX case still fails")
	}
	if authorizeWSRequest(req("wrong", true), wsToken, true) {
		t.Fatal("simple auth accepted a wrong token over TLS")
	}
	if authorizeWSRequest(req("", true), wsToken, true) {
		t.Fatal("simple auth accepted an empty credential over TLS")
	}
}

// The security property that keeps simple auth opt-in — a bound server
// rejecting a raw token over TLS — needs a real TLS session to test, because
// the proof is exported from one and a hand-built ConnectionState cannot
// produce keying material. It is covered end to end in the e2e suite
// (TestWSSSimpleAuthMustAgree), where a real handshake is available.

// isTunnelRequest must still gate on the upgrade and the path before it ever
// looks at the credential, so a browser or a scanner gets the decoy rather than
// an auth check — simple auth or not.
func TestIsTunnelRequestStillGatesOnUpgradeAndPath(t *testing.T) {
	// Right credential, but not a websocket upgrade.
	plain, _ := http.NewRequest("GET", "http://example/channel", nil)
	plain.Header.Set("Authorization", "Bearer "+wsToken)
	if isTunnelRequest(plain, wsToken, true) {
		t.Error("a non-upgrade request was treated as a tunnel request")
	}

	// Upgrade with the right credential, but on a non-tunnel path.
	wrongPath := req(wsToken, false)
	wrongPath.URL.Path = "/"
	if isTunnelRequest(wrongPath, wsToken, true) {
		t.Error("an upgrade on the wrong path was treated as a tunnel request")
	}

	// The genuine article.
	if !isTunnelRequest(req(wsToken, false), wsToken, true) {
		t.Error("a genuine tunnel request was rejected")
	}
}
