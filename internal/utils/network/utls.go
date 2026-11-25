package network

import (
	"context"
	"fmt"
	"net"
	"time"

	utls "github.com/refraction-networking/utls"
)

// Wearing a browser's TLS fingerprint.
//
// A WSS tunnel is meant to be invisible inside ordinary HTTPS, and at the HTTP
// layer it is — it carries a real User-Agent and a plausible path. But the TLS
// ClientHello underneath it is Go's, and Go's ClientHello has a fingerprint all
// its own: the exact cipher list, the curves, the order of the extensions.
// On a filtered route that is enough to tell "a Go program dialling out" from "a
// browser loading a page", and to block the former while leaving the latter
// alone — regardless of how convincing the layers above look.
//
// So the handshake is sent with the fingerprint of a current Chrome instead.
// Chrome's is the most common ClientHello on the wire, so wearing it is how the
// tunnel joins the crowd rather than standing out. Nothing above TLS changes,
// and nothing about trust changes: the certificate is still not verified (the
// tunnel authenticates with its token, exactly as the plain WSS path did), so
// this only alters how the handshake looks, not what it relies on.

// uTLSClientConn completes a TLS handshake over raw whose ClientHello mimics a
// current Chrome build, and returns the encrypted connection.
//
// It returns the concrete *utls.UConn (which is a net.Conn) rather than the
// interface, so the caller can also export keying material from the finished
// session to bind the tunnel credential to it — see wssbind.go.
// http11Only narrows the ALPN offer to what a websocket can actually use. It
// is for the second attempt against a peer that answered the first one with h2
// — see the WSS case in ws_dialer.go — and costs a fingerprint deviation, so it
// is never the first thing tried.
func uTLSClientConn(ctx context.Context, raw net.Conn, serverName string, timeout time.Duration, http11Only bool) (*utls.UConn, error) {
	// Held by reference: uTLS does not copy the config, so it can be adjusted
	// after the handshake to re-enable the session exporter (see below).
	cfg := &utls.Config{
		ServerName: serverName,
		// The tunnel trusts its token, not the certificate — matching the plain
		// WSS path, which skipped verification too. Verifying here would also
		// defeat the point when the server presents a self-signed certificate.
		// The credential is instead bound to the session (wssbind.go), which is
		// what actually keeps an impostor that terminates the TLS out.
		InsecureSkipVerify: true,
	}
	uconn := utls.UClient(raw, cfg, utls.HelloChrome_Auto)

	if http11Only {
		if err := pinClientALPNToHTTP11(uconn); err != nil {
			raw.Close()
			return nil, err
		}
	}

	if timeout > 0 {
		_ = raw.SetDeadline(time.Now().Add(timeout))
	}
	if err := uconn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})

	// The Chrome fingerprint carries the renegotiation_info extension, and
	// applying it flips on renegotiation support — which, as a side effect,
	// disables the RFC 5705 keying-material exporter the credential binding
	// relies on. The extension is already on the wire by now, so the fingerprint
	// is unaffected; turning renegotiation back off re-enables the exporter
	// without changing a single byte that was sent. (We never renegotiate — TLS
	// 1.3 cannot — so nothing else depends on this.)
	cfg.Renegotiation = utls.RenegotiateNever

	return uconn, nil
}

// sniFromAddr picks the server name to present in the handshake. For a hostname
// that is the hostname; for a bare IP it is empty, because a browser sends no
// SNI when it dials an address literal and the fingerprint should match.
func sniFromAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if net.ParseIP(host) != nil {
		return ""
	}
	return host
}

// pinClientALPNToHTTP11 removes h2 from the ClientHello's ALPN offer.
//
// The Chrome fingerprint offers "h2" then "http/1.1", because that is what a
// browser sends. Against our own server that is harmless: it pins http/1.1 on
// its side (see pinHTTP11ALPN) and so never selects h2. Against a CDN it is
// not, because the CDN terminates the TLS and has no such instruction — it sees
// h2 on offer, selects it, and answers the websocket upgrade with an HTTP/2
// SETTINGS frame. What the client reports is:
//
//	malformed HTTP response "\x00\x00\x12\x04\x00\x00\x00\x00\x00..."
//
// which is that frame: three length bytes, type 0x04 (SETTINGS), and a zero
// stream id. Behind Cloudflare the tunnel could not come up at all, while a
// direct connection to the same server worked perfectly.
//
// An RFC 6455 websocket needs the HTTP/1.1 Upgrade mechanism, which HTTP/2 does
// not have — carrying one over h2 is a different protocol (RFC 8441 extended
// CONNECT) that gorilla does not implement. So against a peer that selects h2
// the offer has to be narrowed to what the client can actually use.
//
// What this costs, and why it is not done to everybody:
//
// The hello it produces is a contradiction. Every other field is Chrome's, byte
// for byte, and then the ALPN offers only http/1.1 — which Chrome never does.
// JA3 does not see it, because JA3 hashes extension type ids and not their
// contents; the hello is three bytes shorter and JA3 is unchanged. JA4 does see
// it: its ALPN field goes from "h2" to "h1". So the result is a rare but
// perfectly stable JA4 shared by every tunnel that sends it, always towards the
// same handful of foreign addresses. That is a cheap thing to collect and a
// cheap thing to act on, and this transport exists to be uninteresting.
//
// It was applied to every wss dial for one release. Reverse tunnels started
// dying two or three days in and coming back on a fresh server, which is what
// address-level blocking looks like from the far end. Whether that was the
// cause is not provable from here, but paying a fingerprint deviation on every
// direct connection to solve a problem only CDN users have is the wrong trade
// however it turned out — so it is now the second attempt rather than the
// first, and only a peer that actually answers with h2 gets it.
func pinClientALPNToHTTP11(uconn *utls.UConn) error {
	// The preset's extensions do not exist until the hello is built, and the
	// ALPN list inside it is hardcoded by the fingerprint rather than read from
	// the config — so setting NextProtos alone changes nothing here.
	if err := uconn.BuildHandshakeState(); err != nil {
		return fmt.Errorf("wss: building the client hello: %w", err)
	}
	for _, ext := range uconn.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = []string{"http/1.1"}
		}
	}
	// The handshake re-marshals the hello from these extensions, so the change
	// is on the wire without anything else being touched.
	return nil
}
