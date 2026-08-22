package network

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"
)

// A CDN in front of the server terminates the TLS itself, so the server's own
// ALPN pinning never gets a say — whatever the client offers is what the CDN
// may choose. This stands in for that: a listener that would rather speak h2,
// exactly as Cloudflare does.
func alpnEchoServer(t *testing.T, offers []string) net.Listener {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "cdn.example"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"cdn.example"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		// The order is the point: given the choice this peer takes h2.
		NextProtos: offers,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

// The regression test for a WSS tunnel that would not come up behind
// Cloudflare while working perfectly against the same server's IP.
//
// The client wore a Chrome fingerprint, and a browser offers h2 before
// http/1.1. Our own server pins http/1.1 and so never took the h2 on offer; a
// CDN has no such instruction, took it, and answered the websocket upgrade with
// an HTTP/2 SETTINGS frame — which the HTTP/1.1 response parser reported as
//
//	malformed HTTP response "\x00\x00\x12\x04\x00\x00\x00\x00\x00..."
//
// A websocket cannot be carried over h2 at all, so offering it was the fault.
func TestTheClientNeverNegotiatesHTTP2(t *testing.T) {
	ln := alpnEchoServer(t, []string{"h2", "http/1.1"})

	negotiated := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			negotiated <- "accept failed: " + err.Error()
			return
		}
		defer conn.Close()
		tc := conn.(*tls.Conn)
		if err := tc.HandshakeContext(context.Background()); err != nil {
			negotiated <- "handshake failed: " + err.Error()
			return
		}
		negotiated <- tc.ConnectionState().NegotiatedProtocol
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	uconn, err := uTLSClientConn(context.Background(), raw, "cdn.example", 10*time.Second)
	if err != nil {
		t.Fatalf("the handshake failed: %v", err)
	}
	defer uconn.Close()

	select {
	case got := <-negotiated:
		if got == "h2" {
			t.Fatal("the peer selected h2: the websocket upgrade that follows would be " +
				"answered with an HTTP/2 SETTINGS frame, which is the \"malformed HTTP " +
				"response\" the field reported behind Cloudflare")
		}
		if got != "http/1.1" {
			t.Fatalf("negotiated %q, want http/1.1", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server never completed a handshake")
	}

	if got := uconn.ConnectionState().NegotiatedProtocol; got != "http/1.1" {
		t.Errorf("the client believes it negotiated %q, want http/1.1", got)
	}
}

// A peer that only speaks HTTP/1.1 — our own server — must still work, and must
// not be broken by narrowing the offer.
func TestTheClientStillWorksAgainstAnHTTP11OnlyPeer(t *testing.T) {
	ln := alpnEchoServer(t, []string{"http/1.1"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.(*tls.Conn).HandshakeContext(context.Background())
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	uconn, err := uTLSClientConn(context.Background(), raw, "cdn.example", 10*time.Second)
	if err != nil {
		t.Fatalf("the handshake failed against an http/1.1-only peer: %v", err)
	}
	defer uconn.Close()

	if got := uconn.ConnectionState().NegotiatedProtocol; got != "http/1.1" {
		t.Errorf("negotiated %q, want http/1.1", got)
	}
	<-done
}
