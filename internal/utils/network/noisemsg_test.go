package network

import (
	"bytes"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// pipeMessages is an in-memory message transport: each WriteMessage on one
// end is one ReadMessage on the other.
type pipeMessages struct {
	in, out chan []byte
}

func messagePair() (*pipeMessages, *pipeMessages) {
	a, b := make(chan []byte, 16), make(chan []byte, 16)
	return &pipeMessages{in: a, out: b}, &pipeMessages{in: b, out: a}
}

func (p *pipeMessages) ReadMessage() (int, []byte, error) {
	m, ok := <-p.in
	if !ok {
		return 0, nil, errors.New("closed")
	}
	return wsBinaryMessage, m, nil
}

func (p *pipeMessages) WriteMessage(_ int, data []byte) error {
	p.out <- append([]byte(nil), data...)
	return nil
}

func (p *pipeMessages) SetReadDeadline(time.Time) error  { return nil }
func (p *pipeMessages) SetWriteDeadline(time.Time) error { return nil }
func (p *pipeMessages) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (p *pipeMessages) RemoteAddr() net.Addr             { return &net.TCPAddr{} }

func boxPair(t *testing.T, clientToken, serverToken string) (*NoiseBox, *NoiseBox, error, error) {
	t.Helper()
	a, b := messagePair()
	var (
		cb, sb     *NoiseBox
		cerr, serr error
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); cb, cerr = NoiseMessageClient(a, clientToken, 2*time.Second) }()
	go func() { defer wg.Done(); sb, serr = NoiseMessageServer(b, serverToken, 2*time.Second) }()
	// A refused handshake leaves the client waiting for a reply that never
	// comes; closing its inbox ends that wait.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(a.in)
		<-done
	}
	return cb, sb, cerr, serr
}

// Messages keep their boundaries and contents, both ways, including an empty
// one and one larger than a single Noise record.
func TestNoiseBoxCarriesMessagesWhole(t *testing.T) {
	cb, sb, cerr, serr := boxPair(t, "token-one", "token-one")
	if cerr != nil || serr != nil {
		t.Fatalf("handshake: client %v, server %v", cerr, serr)
	}
	for _, msg := range [][]byte{{}, []byte("x"), bytes.Repeat([]byte("abc"), 70000)} {
		sealed, err := cb.Seal(msg)
		if err != nil {
			t.Fatal(err)
		}
		if len(msg) > 8 && bytes.Contains(sealed, msg[:8]) {
			t.Fatal("the plaintext is visible in the sealed message")
		}
		got, err := sb.Open(sealed)
		if err != nil {
			t.Fatalf("open %d bytes: %v", len(msg), err)
		}
		if !bytes.Equal(got, msg) {
			t.Fatalf("a %d-byte message came back as %d bytes", len(msg), len(got))
		}
		back, _ := sb.Seal(msg)
		if got, err := cb.Open(back); err != nil || !bytes.Equal(got, msg) {
			t.Fatalf("server to client: %v", err)
		}
	}
}

// A changed byte is refused, not delivered.
func TestNoiseBoxRefusesATamperedMessage(t *testing.T) {
	cb, sb, cerr, serr := boxPair(t, "token-one", "token-one")
	if cerr != nil || serr != nil {
		t.Fatalf("handshake: client %v, server %v", cerr, serr)
	}
	sealed, _ := cb.Seal([]byte("hello"))
	sealed[len(sealed)-1] ^= 1
	if _, err := sb.Open(sealed); err == nil {
		t.Fatal("a tampered message was opened")
	}
}

// A peer with a different token gets no box.
func TestNoiseBoxNeedsTheSameToken(t *testing.T) {
	_, _, cerr, serr := boxPair(t, "token-one", "token-two")
	if serr == nil {
		t.Fatal("the server completed a handshake with a client holding another token")
	}
	if cerr == nil {
		t.Fatal("the client completed a handshake the server refused")
	}
}
