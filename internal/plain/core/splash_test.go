package core

import (
	"bytes"
	"io"
	"net"
	"testing"
)

// The first records come out padded to lengths that say nothing about them,
// everything arrives intact, and after splashRecords the stream is passed
// through unframed.
func TestSplashPadsTheFirstRecordsThenGetsOutOfTheWay(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	w := newSplash(a)

	var wire bytes.Buffer
	tee := make(chan []byte, 64)
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, err := b.Read(buf)
			if n > 0 {
				tee <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				close(tee)
				return
			}
		}
	}()

	msgs := [][]byte{[]byte("hi"), bytes.Repeat([]byte{7}, 517), bytes.Repeat([]byte{9}, 3000)}
	for i := 0; i < splashRecords+3; i++ {
		msgs = append(msgs, []byte{byte(i)})
	}
	go func() {
		for _, m := range msgs {
			_, _ = w.Write(m)
		}
		a.Close()
	}()
	for chunk := range tee {
		wire.Write(chunk)
	}

	// Read it back through a splash reader.
	c1, c2 := net.Pipe()
	go func() { _, _ = c1.Write(wire.Bytes()); c1.Close() }()
	got, err := io.ReadAll(newSplash(c2))
	if err != nil {
		t.Fatal(err)
	}
	if want := bytes.Join(msgs, nil); !bytes.Equal(got, want) {
		t.Fatalf("read back %d bytes, want %d", len(got), len(want))
	}

	// The first record ("hi") went out at least splashMin long; the tail
	// after splashRecords went out as it is.
	raw := wire.Bytes()
	if l := splashHeader + 2 + (int(raw[2])<<8 | int(raw[3])); l < splashMin {
		t.Fatalf("a 2-byte record went out %d bytes long, want at least %d", l, splashMin)
	}
	tail := raw[len(raw)-3:]
	if !bytes.Equal(tail, []byte{byte(splashRecords), byte(splashRecords + 1), byte(splashRecords + 2)}) {
		t.Fatalf("the records after splash were not passed through: % x", tail)
	}
}

func TestSplashMessageRoundTrip(t *testing.T) {
	for _, m := range [][]byte{{}, []byte("x"), bytes.Repeat([]byte("a"), 100000)} {
		p := splashMessage(m)
		if len(m) < splashMin && len(p) < splashMin {
			t.Fatalf("a %d-byte message was padded to only %d", len(m), len(p))
		}
		back, err := unsplashMessage(p)
		if err != nil || !bytes.Equal(back, m) {
			t.Fatalf("round trip of %d bytes: %v", len(m), err)
		}
	}
	if _, err := unsplashMessage([]byte{0, 0, 0, 9, 1}); err == nil {
		t.Fatal("a message claiming more data than it holds was accepted")
	}
}
