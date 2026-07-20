package network

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// The whole point of the browser fingerprint is that the ClientHello on the
// wire is not Go's. These tests capture the actual first flight and prove it:
// the mimicked handshake carries a GREASE cipher suite, which Chrome emits and
// Go's standard library never does — so the presence of one is a reliable,
// self-checking discriminator between "a browser" and "a Go program".

// captureClientHello starts a throwaway listener, runs dial against it, and
// returns the first bytes the dialer sent. The handshake never completes — the
// listener is not a TLS server — but the ClientHello is the first thing out.
func captureClientHello(t *testing.T, dial func(addr string)) []byte {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer conn.Close()
		buf := make([]byte, 2048)
		n, _ := conn.Read(buf)
		got <- buf[:n]
	}()

	go dial(ln.Addr().String())

	select {
	case b := <-got:
		if len(b) == 0 {
			t.Fatal("captured no ClientHello")
		}
		return b
	case <-time.After(5 * time.Second):
		t.Fatal("timed out capturing the ClientHello")
		return nil
	}
}

func TestWSSHandshakeWearsABrowserFingerprint(t *testing.T) {
	hello := captureClientHello(t, func(addr string) {
		raw, err := net.Dial("tcp", addr)
		if err != nil {
			return
		}
		_, _ = uTLSClientConn(context.Background(), raw, "example.com", time.Second)
	})

	if hello[0] != 0x16 {
		t.Fatalf("first byte %#x is not a TLS handshake record", hello[0])
	}
	suites, ok := clientHelloCipherSuites(hello)
	if !ok {
		t.Fatal("could not parse the cipher suites out of the ClientHello")
	}
	if !containsGREASE(suites) {
		t.Errorf("the mimicked ClientHello carries no GREASE cipher suite, so it does "+
			"not look like a browser: %#x", suites)
	}
}

// The control: Go's own ClientHello must NOT contain GREASE, or the test above
// would pass for the wrong reason — it would not actually be distinguishing a
// browser fingerprint from Go's.
func TestGoHandshakeHasNoGREASE(t *testing.T) {
	hello := captureClientHello(t, func(addr string) {
		raw, err := net.Dial("tcp", addr)
		if err != nil {
			return
		}
		c := tls.Client(raw, &tls.Config{ServerName: "example.com", InsecureSkipVerify: true})
		_ = c.HandshakeContext(context.Background())
	})

	suites, ok := clientHelloCipherSuites(hello)
	if !ok {
		t.Fatal("could not parse the cipher suites out of Go's ClientHello")
	}
	if containsGREASE(suites) {
		t.Error("Go's standard ClientHello unexpectedly contains GREASE — the GREASE " +
			"check cannot tell the two apart")
	}
}

// clientHelloCipherSuites parses the offered cipher suites out of a ClientHello,
// with enough bounds checking that a malformed capture returns ok=false rather
// than panicking.
func clientHelloCipherSuites(b []byte) ([]uint16, bool) {
	// TLS record header (5) + handshake type (1) + handshake length (3) +
	// client_version (2) + random (32) = 43, then session id.
	const sessionIDLenOffset = 43
	if len(b) <= sessionIDLenOffset {
		return nil, false
	}
	p := sessionIDLenOffset
	sidLen := int(b[p])
	p += 1 + sidLen
	if p+2 > len(b) {
		return nil, false
	}
	csLen := int(binary.BigEndian.Uint16(b[p : p+2]))
	p += 2
	if csLen%2 != 0 || p+csLen > len(b) {
		return nil, false
	}
	suites := make([]uint16, 0, csLen/2)
	for i := 0; i < csLen; i += 2 {
		suites = append(suites, binary.BigEndian.Uint16(b[p+i:p+i+2]))
	}
	return suites, true
}

// containsGREASE reports whether any value is a GREASE placeholder. GREASE
// values have both bytes equal with a low nibble of 0xA: 0x0A0A, 0x1A1A, …,
// 0xFAFA. Chrome sprinkles them in; Go never emits them.
func containsGREASE(values []uint16) bool {
	for _, v := range values {
		hi, lo := byte(v>>8), byte(v)
		if hi == lo && lo&0x0f == 0x0a {
			return true
		}
	}
	return false
}
