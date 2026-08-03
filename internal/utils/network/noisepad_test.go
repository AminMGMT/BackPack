package network

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/flynn/noise"
)

const padToken = "a-real-looking-tunnel-token-0123456789"

// Both ends of a current build must agree to pad. Everything else here rests on
// that having actually happened, so it is checked directly rather than inferred
// from the traffic.
func TestPaddingIsNegotiatedOnBothSides(t *testing.T) {
	cc, sc, cerr, serr := noisePair(t, padToken, padToken)
	if cerr != nil || serr != nil {
		t.Fatalf("handshake failed: client=%v server=%v", cerr, serr)
	}
	defer cc.Close()
	defer sc.Close()

	if !cc.(*noiseConn).pad {
		t.Error("the initiator did not turn padding on")
	}
	if !sc.(*noiseConn).pad {
		t.Error("the responder did not turn padding on")
	}
}

// Padding must not change what comes out the other end — the whole point is
// that it is invisible above the record layer.
func TestPaddedStreamRoundTripsExactly(t *testing.T) {
	cc, sc, cerr, serr := noisePair(t, padToken, padToken)
	if cerr != nil || serr != nil {
		t.Fatalf("handshake failed: client=%v server=%v", cerr, serr)
	}
	defer sc.Close()

	// Several records' worth, so the chunking, the padding and the reassembly
	// all get exercised together.
	payload := make([]byte, 400*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}

	go func() {
		io.Copy(cc, bytes.NewReader(payload))
		cc.Close()
	}()

	got, err := io.ReadAll(sc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("padding corrupted the stream: got %d bytes, want %d", len(got), len(payload))
	}
}

// Writing the same payload repeatedly must not produce the same record length
// twice over — that is the entire purpose. Without padding, every one of these
// records would be identical on the wire.
func TestIdenticalWritesProduceDifferentRecordSizes(t *testing.T) {
	// The "peer" here is a plain socket, so the exact bytes the record layer
	// emits can be measured.
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
		sCh <- res{raw, nil}
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cCh := make(chan res, 1)
	go func() {
		conn, err := NoiseClientConn(raw, padToken, 3*time.Second)
		cCh <- res{conn, err}
	}()

	server := <-sCh
	if server.err != nil {
		t.Fatalf("accept: %v", server.err)
	}
	defer server.conn.Close()

	// Answer the handshake as the responder would, advertising a version so
	// the initiator turns padding on.
	if err := respondToHandshake(server.conn); err != nil {
		t.Fatalf("responding to the handshake: %v", err)
	}

	client := <-cCh
	if client.err != nil {
		t.Fatalf("handshake: %v", client.err)
	}
	defer client.conn.Close()
	if !client.conn.(*noiseConn).pad {
		t.Fatal("padding was not negotiated, so this proves nothing")
	}

	// The same one-byte write, many times. A heartbeat, in other words.
	const rounds = 40
	go func() {
		for i := 0; i < rounds; i++ {
			client.conn.Write([]byte{0x01})
		}
	}()

	sizes := map[uint16]int{}
	server.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for i := 0; i < rounds; i++ {
		var hdr [2]byte
		if _, err := io.ReadFull(server.conn, hdr[:]); err != nil {
			t.Fatalf("reading record %d: %v", i, err)
		}
		n := binary.BigEndian.Uint16(hdr[:])
		if _, err := io.ReadFull(server.conn, make([]byte, n)); err != nil {
			t.Fatalf("draining record %d: %v", i, err)
		}
		sizes[n]++
	}

	if len(sizes) < 5 {
		t.Fatalf("%d identical writes produced only %d distinct record sizes: %v", rounds, len(sizes), sizes)
	}
}

// respondToHandshake plays the responder side well enough to complete an
// NNpsk0 exchange and advertise a version, without using the record layer under
// test to do it.
func respondToHandshake(raw net.Conn) error {
	conn, err := NoiseServerConn(raw, padToken, 3*time.Second)
	if err != nil {
		return err
	}
	// The wrapper is discarded on purpose: the test reads the raw socket to see
	// the record framing. Keeping the underlying conn open is all that matters.
	_ = conn
	return nil
}

// A peer too old to negotiate must still work, in both directions, with padding
// simply off. Version 0 is what an empty handshake payload reads as.
func TestPaddingOffAgainstAPeerThatDoesNotAdvertise(t *testing.T) {
	if v := versionFromPayload(nil); v != 0 {
		t.Fatalf("an absent payload read as version %d, want 0", v)
	}
	if v := versionFromPayload([]byte{}); v != 0 {
		t.Fatalf("an empty payload read as version %d, want 0", v)
	}
	if v := versionFromPayload([]byte{7}); v != 7 {
		t.Fatalf("a payload of 7 read as version %d", v)
	}
}

// unpad has to refuse a record that does not describe itself rather than
// slicing past the end of it.
func TestUnpadRejectsAMalformedRecord(t *testing.T) {
	c := &noiseConn{pad: true}

	if _, err := c.unpad(nil); err == nil {
		t.Error("an empty padded record was accepted")
	}
	// Claims 200 bytes of padding inside a 10-byte record.
	if _, err := c.unpad(append([]byte{200}, make([]byte, 9)...)); err == nil {
		t.Error("a record claiming more padding than it holds was accepted")
	}
	// Exactly consistent: 1 length byte + 4 payload + 5 padding.
	got, err := c.unpad(append([]byte{5}, []byte("hello\x00\x00\x00\x00\x00")...))
	if err != nil {
		t.Fatalf("a well-formed record was rejected: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("unpadded to %q, want %q", got, "hello")
	}
}

// With padding off, unpad must hand the record back untouched — a peer that
// does not pad sends no length byte to strip.
func TestUnpadIsAPassthroughWhenPaddingIsOff(t *testing.T) {
	c := &noiseConn{pad: false}
	in := []byte{5, 1, 2, 3}
	got, err := c.unpad(in)
	if err != nil {
		t.Fatalf("unpad: %v", err)
	}
	if !bytes.Equal(got, in) {
		t.Fatalf("unpad changed the record: %v", got)
	}
}

// The filler must never carry the previous record's plaintext. The buffer is
// reused, so this is a real leak if the zeroing is dropped.
func TestPaddingFillerIsZeroedBetweenRecords(t *testing.T) {
	c := &noiseConn{pad: true}

	// A large record first, to fill the buffer with recognisable bytes.
	secret := bytes.Repeat([]byte("S"), 4096)
	first := c.plaintext(secret)
	if len(first) < 1+len(secret) {
		t.Fatalf("plaintext returned %d bytes for a %d-byte payload", len(first), len(secret))
	}

	// Then a small one. Its filler comes from the same buffer.
	for i := 0; i < 50; i++ {
		out := c.plaintext([]byte("x"))
		padLen := int(out[0])
		filler := out[len(out)-padLen:]
		if bytes.Contains(filler, []byte("SS")) {
			t.Fatal("the filler carried the previous record's plaintext")
		}
		for _, b := range filler {
			if b != 0 {
				t.Fatalf("the filler is not zeroed: found %d", b)
			}
		}
	}
}

// plaintext must round-trip through unpad for every padding length it can draw.
func TestPlaintextAndUnpadAgree(t *testing.T) {
	c := &noiseConn{pad: true}
	payload := []byte("the quick brown fox")

	for i := 0; i < 500; i++ {
		got, err := c.unpad(c.plaintext(payload))
		if err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("round %d: got %q, want %q", i, got, payload)
		}
	}
}

// A padded record must never exceed what a Noise message can hold, whatever the
// draw. Getting this wrong would fail only on the largest writes.
func TestPaddedRecordNeverExceedsTheNoiseCeiling(t *testing.T) {
	c := &noiseConn{pad: true}
	chunk := make([]byte, noisePaddedMaxPayload)
	for i := 0; i < 200; i++ {
		if n := len(c.plaintext(chunk)); n > noiseMaxPayload {
			t.Fatalf("a full-sized padded record is %d bytes, over the %d ceiling", n, noiseMaxPayload)
		}
	}
}

// legacyInitiator performs the handshake exactly as a build that predates the
// version negotiation did: an empty payload in the first message, and no
// interest in whatever comes back.
//
// This is what proves the compatibility claim rather than assuming it. The
// negotiation was designed so an old peer's silence reads as version 0, and the
// only way to know that holds is to be silent in the same way.
func legacyInitiator(raw net.Conn, token string) (net.Conn, error) {
	psk, err := noisePSK(token)
	if err != nil {
		return nil, err
	}
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:           noiseSuite,
		Pattern:               noise.HandshakeNN,
		Initiator:             true,
		Prologue:              []byte(noisePrologue),
		PresharedKey:          psk,
		PresharedKeyPlacement: 0,
	})
	if err != nil {
		return nil, err
	}

	// The old code passed nil here. That is the whole difference.
	msg, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, err
	}
	if err := writeNoiseFrame(raw, msg); err != nil {
		return nil, err
	}
	in, err := readNoiseFrame(raw)
	if err != nil {
		return nil, err
	}
	_, cs0, cs1, err := hs.ReadMessage(nil, in)
	if err != nil {
		return nil, err
	}
	// No padding: an old build had none.
	return &noiseConn{Conn: raw, send: cs0, recv: cs1, pad: false}, nil
}

// A current server and a client too old to negotiate must still carry traffic,
// with padding simply off.
func TestCurrentServerTalksToALegacyClient(t *testing.T) {
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
		conn, err := NoiseServerConn(raw, padToken, 3*time.Second)
		sCh <- res{conn, err}
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client, err := legacyInitiator(raw, padToken)
	if err != nil {
		t.Fatalf("the legacy handshake was rejected: %v", err)
	}
	defer client.Close()

	server := <-sCh
	if server.err != nil {
		t.Fatalf("the server rejected a legacy client: %v", server.err)
	}
	defer server.conn.Close()

	if server.conn.(*noiseConn).pad {
		t.Fatal("the server padded against a client that cannot unpad")
	}

	payload := bytes.Repeat([]byte("legacy-"), 30000) // several records
	go func() {
		io.Copy(client, bytes.NewReader(payload))
		client.Close()
	}()

	got, err := io.ReadAll(server.conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("the stream was corrupted: got %d bytes, want %d", len(got), len(payload))
	}
}

// And the same in the other direction: a current server writing to a legacy
// client must send records it can actually read.
func TestCurrentServerWritesUnpaddedToALegacyClient(t *testing.T) {
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
		conn, err := NoiseServerConn(raw, padToken, 3*time.Second)
		sCh <- res{conn, err}
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client, err := legacyInitiator(raw, padToken)
	if err != nil {
		t.Fatalf("the legacy handshake was rejected: %v", err)
	}
	defer client.Close()

	server := <-sCh
	if server.err != nil {
		t.Fatalf("the server rejected a legacy client: %v", server.err)
	}

	payload := bytes.Repeat([]byte("server-says-"), 20000)
	go func() {
		io.Copy(server.conn, bytes.NewReader(payload))
		server.conn.Close()
	}()

	got, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("the stream was corrupted: got %d bytes, want %d", len(got), len(payload))
	}
}
