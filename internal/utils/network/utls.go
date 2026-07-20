package network

import (
	"context"
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
func uTLSClientConn(ctx context.Context, raw net.Conn, serverName string, timeout time.Duration) (*utls.UConn, error) {
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
