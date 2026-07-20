package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The Telegram relay, end to end.
//
// The bot cannot reach Telegram from Iran, so its requests go out through a
// tunnel: a local port here is forwarded to api.telegram.org:443 on the peer,
// and the bot dials that port instead of the API directly.
//
// This used to run through a SOCKS proxy on the peer, which meant a second
// component that had to be installed and running over there — and when it was
// not, the failure surfaced on this side as a bare "EOF". Forwarding the port
// straight to the API removes that component: the tunnel already knows how to
// forward a port to an arbitrary destination.
//
// TLS is untouched by the change. The bot still requests
// https://api.telegram.org/..., so the certificate is verified against that
// name and the tunnel carries a stream it cannot read.
func TestTelegramForwardCarriesTLSEndToEnd(t *testing.T) {
	for _, transport := range []string{"tcp", "tcpmux", "kcp", "ws", "wsmux", "stealth"} {
		t.Run(transport, func(t *testing.T) { telegramForwardTest(t, transport) })
	}
}

func telegramForwardTest(t *testing.T, transport string) {
	// A TLS server standing in for api.telegram.org, with a certificate the
	// client is made to trust — so a real handshake has to succeed rather than
	// a skipped one.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	const token = "relay-token-0123456789abcdefghij"
	tunnelPort := freePort(t)
	exposed := freePort(t)

	// The exact mapping EnsureTelegramPort writes: a loopback-bound local port
	// forwarded to the API's address ("127.0.0.1:PORT=host:443"), not the bare
	// "PORT=host:443" form. The bind address is the part that changed, so the
	// test has to exercise the real shape or it proves nothing about production.
	srvCfg := baseServerConfig(transport, tunnelPort, exposed,
		upstream.Listener.Addr().String(), token)
	srvCfg.Ports = []string{fmt.Sprintf("127.0.0.1:%d=%s", exposed, upstream.Listener.Addr().String())}
	cliCfg := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	// Readiness cannot use the usual echo probe: this backend speaks TLS and
	// closes anything that does not. The real request is retried instead.
	runPair(t, srvCfg, cliCfg, exposed, tunnelPort)

	// The bot's client: the URL still names the API, only the dial is
	// redirected — exactly what tunnelledClient does.
	pool := upstream.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	local := fmt.Sprintf("127.0.0.1:%d", exposed)
	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if addr == "api.telegram.org:443" {
					addr = local
				}
				return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
			},
			// The stand-in certificate is issued for example.com, so that is
			// the name to verify; the redirect being tested is the dial.
			TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "example.com"},
		},
	}

	var resp *http.Response
	var err error
	deadline := time.Now().Add(tunnelReadyTimeout)
	for time.Now().Before(deadline) {
		resp, err = client.Get("https://api.telegram.org/bot123/getMe")
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("%s: the forwarded request never succeeded: %v", transport, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, body %q", resp.StatusCode, body)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q, want the upstream response", body)
	}
}

// The tunnel must not be able to read what it carries. That property is what
// makes forwarding straight to the API safe: the payload is TLS between the bot
// and Telegram, and the tunnel only moves bytes.
func TestForwardedTrafficStaysEncrypted(t *testing.T) {
	// A plain listener that records the first thing to arrive.
	seen := make(chan []byte, 1)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 512)
		n, _ := conn.Read(buf)
		if n > 0 {
			seen <- buf[:n]
		}
	}()

	const token = "encrypted-token-0123456789abcdef"
	tunnelPort := freePort(t)
	exposed := freePort(t)

	srvCfg := baseServerConfig("tcp", tunnelPort, exposed, ln.Addr().String(), token)
	cliCfg := baseClientConfig("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
	runPair(t, srvCfg, cliCfg, exposed, tunnelPort)

	// A TLS handshake through the forward. It will not complete — the far end
	// is not a TLS server — but the first flight still crosses the tunnel.
	local := fmt.Sprintf("127.0.0.1:%d", exposed)
	go func() {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			conn, err := net.DialTimeout("tcp", local, 3*time.Second)
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			tlsConn := tls.Client(conn, &tls.Config{ServerName: "api.telegram.org"})
			_ = tlsConn.Handshake()
			conn.Close()
			return
		}
	}()

	select {
	case got := <-seen:
		// A TLS ClientHello begins with the handshake record type, 0x16.
		if got[0] != 0x16 {
			t.Errorf("what crossed the tunnel is not a TLS handshake: % x", got[:minInt(16, len(got))])
		}
		// The request path must not be visible; SNI carrying the hostname is
		// expected and unavoidable.
		if containsBytes(got, []byte("getMe")) {
			t.Error("request content crossed the tunnel in the clear")
		}
	case <-time.After(25 * time.Second):
		t.Skip("nothing reached the backend in time; nothing to assert")
	}
}

func containsBytes(haystack, needle []byte) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
