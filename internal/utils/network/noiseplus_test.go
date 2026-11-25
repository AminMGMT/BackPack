package network

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func plusPair(t *testing.T) (net.Conn, net.Conn, *bytes.Buffer) {
	t.Helper()
	a, b := net.Pipe()
	// Record what the client puts on the wire.
	var wire bytes.Buffer
	var mu sync.Mutex
	tap := &tapConn{Conn: a, w: &wire, mu: &mu}
	var (
		c, s       net.Conn
		cerr, serr error
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); c, cerr = StealthPlusClientConn(tap, "plus-token", 3*time.Second) }()
	go func() { defer wg.Done(); s, serr = StealthPlusServerConn(b, "plus-token", 3*time.Second) }()
	wg.Wait()
	if cerr != nil || serr != nil {
		t.Fatalf("handshake: client %v, server %v", cerr, serr)
	}
	return c, s, &wire
}

type tapConn struct {
	net.Conn
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (t *tapConn) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.w.Write(p)
	t.mu.Unlock()
	return t.Conn.Write(p)
}

// Data arrives intact both ways, small and large.
func TestStealthPlusCarriesData(t *testing.T) {
	c, s, _ := plusPair(t)
	defer c.Close()
	defer s.Close()
	for _, msg := range [][]byte{[]byte("x"), bytes.Repeat([]byte("ab"), 100000)} {
		go func() { _, _ = c.Write(msg) }()
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(s, got); err != nil || !bytes.Equal(got, msg) {
			t.Fatalf("%d bytes: %v", len(msg), err)
		}
		go func() { _, _ = s.Write(msg) }()
		if _, err := io.ReadFull(c, got); err != nil || !bytes.Equal(got, msg) {
			t.Fatalf("back, %d bytes: %v", len(msg), err)
		}
	}
}

// Nothing on the wire reads as stealth's framing: the first message is not a
// plain length followed by that many bytes, and a one-byte write does not go
// out as a short record.
func TestStealthPlusLeavesNoFramingOnTheWire(t *testing.T) {
	c, s, wire := plusPair(t)
	defer c.Close()
	defer s.Close()
	go func() { _, _ = io.Copy(io.Discard, s) }()
	if _, err := c.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	raw := wire.Bytes()
	// Stealth's first frame opens with its length in the clear: 2 bytes,
	// then the ephemeral key. Under the mask, those two bytes are not the
	// length of what follows.
	if n := int(binary.BigEndian.Uint16(raw[plusNonce:])); n == len(raw)-plusNonce-2 {
		t.Fatalf("the first two bytes after the nonce are the frame length %d in the clear", n)
	}
	if len(raw) < plusNonce+64 {
		t.Fatalf("only %d bytes went out for a handshake and one byte of data", len(raw))
	}
	// The one-byte write: everything after the handshake is the record, and
	// shaping makes it far longer than stealth's 1+2+16 bytes.
	if len(raw) < plusNonce+32+96 {
		t.Fatalf("the shaped record looks short: %d bytes total", len(raw))
	}
}

// A peer with another token is refused.
func TestStealthPlusNeedsTheToken(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	errs := make(chan error, 2)
	go func() { _, err := StealthPlusClientConn(a, "one", time.Second); errs <- err }()
	go func() { _, err := StealthPlusServerConn(b, "two", time.Second); b.Close(); errs <- err }()
	first, second := <-errs, <-errs
	if first == nil && second == nil {
		t.Fatal("two ends with different tokens completed a stealth+ handshake")
	}
}

// The message form carries messages whole and masks them.
func TestStealthPlusMessages(t *testing.T) {
	a, b := messagePair()
	var (
		cb, sb     *NoiseBox
		cerr, serr error
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); cb, cerr = StealthPlusMessageClient(a, "plus-token", 2*time.Second) }()
	go func() { defer wg.Done(); sb, serr = StealthPlusMessageServer(b, "plus-token", 2*time.Second) }()
	wg.Wait()
	if cerr != nil || serr != nil {
		t.Fatalf("handshake: %v / %v", cerr, serr)
	}
	for _, msg := range [][]byte{{}, []byte("hello"), bytes.Repeat([]byte("z"), 90000)} {
		sealed, err := cb.Seal(msg)
		if err != nil {
			t.Fatal(err)
		}
		if len(msg) < plusShapeMax && len(sealed) < 96 {
			t.Fatalf("a %d-byte message was sealed into only %d bytes", len(msg), len(sealed))
		}
		got, err := sb.Open(sealed)
		if err != nil || !bytes.Equal(got, msg) {
			t.Fatalf("%d bytes: %v", len(msg), err)
		}
	}
}
