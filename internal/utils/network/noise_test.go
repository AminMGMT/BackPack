package network

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// noisePair runs the two handshake sides against each other over a real
// loopback TCP connection and returns the wrapped connections, or the errors
// they failed with. A real socket (rather than net.Pipe) is used deliberately:
// net.Pipe is unbuffered, so a Write blocks until a matching Read, which would
// deadlock any test that writes and reads on the same goroutine — and it is not
// how the transport is used in production anyway.
func noisePair(t *testing.T, clientToken, serverToken string) (net.Conn, net.Conn, error, error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type res struct {
		conn net.Conn
		err  error
	}
	sCh := make(chan res, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			sCh <- res{nil, err}
			return
		}
		conn, err := NoiseServerConn(raw, serverToken, 3*time.Second)
		sCh <- res{conn, err}
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cCh := make(chan res, 1)
	go func() {
		conn, err := NoiseClientConn(raw, clientToken, 3*time.Second)
		cCh <- res{conn, err}
	}()

	cr := <-cCh
	sr := <-sCh
	return cr.conn, sr.conn, cr.err, sr.err
}

func TestStealthRoundTrip(t *testing.T) {
	const token = "a-real-looking-tunnel-token-0123456789"
	cc, sc, cerr, serr := noisePair(t, token, token)
	if cerr != nil || serr != nil {
		t.Fatalf("handshake failed: client=%v server=%v", cerr, serr)
	}
	defer cc.Close()
	defer sc.Close()

	// Client -> server, including a payload larger than one Noise record, to
	// exercise the chunking on write and the reassembly on read. Written on the
	// client end, read on the server end — the closing write shuts cc so the
	// reader sees EOF rather than blocking forever.
	big := bytes.Repeat([]byte("backpack-"), 20000) // ~180 KB, several records
	go func() {
		io.Copy(cc, bytes.NewReader(big))
		cc.Close()
	}()

	got, err := io.ReadAll(sc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, big) {
		t.Fatalf("round trip corrupted the stream: got %d bytes, want %d", len(got), len(big))
	}
}

func TestStealthBothDirections(t *testing.T) {
	const token = "another-token-abcdefghijklmnop-0123"
	cc, sc, cerr, serr := noisePair(t, token, token)
	if cerr != nil || serr != nil {
		t.Fatalf("handshake failed: client=%v server=%v", cerr, serr)
	}
	defer cc.Close()
	defer sc.Close()

	for _, tc := range []struct {
		from, to net.Conn
		msg      string
	}{
		{cc, sc, "hello from the client"},
		{sc, cc, "hello back from the server"},
	} {
		if _, err := tc.from.Write([]byte(tc.msg)); err != nil {
			t.Fatalf("write: %v", err)
		}
		buf := make([]byte, len(tc.msg))
		if _, err := io.ReadFull(tc.to, buf); err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(buf) != tc.msg {
			t.Errorf("got %q, want %q", buf, tc.msg)
		}
	}
}

// A peer without the token must not complete the handshake. This is the whole
// point: it is what makes the port answer nothing to a scan.
func TestStealthWrongTokenIsRejected(t *testing.T) {
	cc, sc, cerr, serr := noisePair(t, "the-correct-token-0123456789abcd", "a-completely-different-token-xyz")
	if cerr == nil && serr == nil {
		if cc != nil {
			cc.Close()
		}
		if sc != nil {
			sc.Close()
		}
		t.Fatal("a mismatched token completed the handshake — the transport would authenticate anyone")
	}
}

// What crosses the wire during the handshake must not be a recognisable
// protocol. This is what a censor's deep packet inspection sees first, and the
// transport's reason to exist is that there is nothing there to match.
func TestStealthHandshakeHasNoFingerprint(t *testing.T) {
	const token = "fingerprint-token-0123456789abcdefgh"
	c, s := net.Pipe()

	// Capture the initiator's first flight by reading the raw pipe on the other
	// side while the client speaks.
	firstFrame := make(chan []byte, 1)
	go func() {
		// The frame is a 2-byte length prefix followed by the Noise message.
		hdr := make([]byte, 2)
		if _, err := io.ReadFull(s, hdr); err != nil {
			firstFrame <- nil
			return
		}
		n := int(hdr[0])<<8 | int(hdr[1])
		body := make([]byte, n)
		io.ReadFull(s, body)
		firstFrame <- body
		s.Close()
	}()

	go func() { _, _ = NoiseClientConn(c, token, time.Second) }()

	select {
	case body := <-firstFrame:
		if len(body) == 0 {
			t.Fatal("captured no handshake bytes")
		}
		// Not a TLS record (0x16 handshake), not an HTTP verb, not SSH.
		if body[0] == 0x16 {
			t.Error("the first byte is a TLS handshake record type — that is a fingerprint")
		}
		for _, p := range []string{"GET ", "POST", "HTTP", "SSH-", "CONNECT"} {
			if bytes.HasPrefix(body, []byte(p)) {
				t.Errorf("the handshake begins with %q — a recognisable protocol", p)
			}
		}
		// The ephemeral key that opens the message should look random: reject an
		// all-zero or otherwise trivially low-entropy prefix.
		if isLowEntropy(body[:min(32, len(body))]) {
			t.Error("the opening bytes are not high-entropy; they would stand out")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out capturing the handshake")
	}
}

// isLowEntropy is a crude check: it flags an all-equal byte run, which is enough
// to catch a broken handshake that sends zeros or a constant.
func isLowEntropy(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	for _, x := range b {
		if x != b[0] {
			return false
		}
	}
	return true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
