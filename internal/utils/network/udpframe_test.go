package network

import (
	"bytes"
	"context"
	"net"
	"time"

	"github.com/backpack/backpack/internal/tunnel/limits"
	"io"
	"testing"
)

// The mark is what tells one end that a forwarded flow is datagrams. It has to
// survive a round trip exactly, and it must never fire on an ordinary target —
// a TCP forward that was mistaken for UDP would be dialled with the wrong
// protocol and never connect.
func TestUDPMarkRoundTrips(t *testing.T) {
	for _, addr := range []string{"443", "127.0.0.1:443", "10.0.0.5:8080", "8443|127.0.0.1:8444"} {
		target, isUDP := SplitUDPTarget(MarkUDP(addr))
		if !isUDP {
			t.Errorf("a marked target %q did not read back as UDP", addr)
		}
		if target != addr {
			t.Errorf("the mark changed the target: %q became %q", addr, target)
		}

		if target, isUDP := SplitUDPTarget(addr); isUDP || target != addr {
			t.Errorf("plain target %q was read as UDP", addr)
		}
	}
}

// A datagram is a message: what is written as one frame must be read back as
// one frame of exactly that length, whatever the stream does in between.
func TestDatagramFramingPreservesBoundaries(t *testing.T) {
	payloads := [][]byte{
		{},
		[]byte("a"),
		bytes.Repeat([]byte("x"), 1400),
		bytes.Repeat([]byte{0xff, 0x00}, 8000),
		bytes.Repeat([]byte("m"), MaxDatagram),
	}

	var buf bytes.Buffer
	for _, p := range payloads {
		if err := WriteDatagram(&buf, p); err != nil {
			t.Fatalf("writing a %d byte datagram: %v", len(p), err)
		}
	}

	// Read them back through a reader that hands over one byte at a time, which
	// is the worst case a stream can present and the one a naive parser gets
	// wrong.
	r := iotest{r: &buf}
	out := make([]byte, MaxDatagram)
	for _, want := range payloads {
		n, err := ReadDatagram(&r, out)
		if err != nil {
			t.Fatalf("reading a %d byte datagram: %v", len(want), err)
		}
		if n != len(want) {
			t.Fatalf("datagram came back as %d bytes, sent %d", n, len(want))
		}
		if !bytes.Equal(out[:n], want) {
			t.Fatalf("a %d byte datagram came back corrupted", len(want))
		}
	}
	if _, err := ReadDatagram(&r, out); err != io.EOF {
		t.Errorf("reading past the last datagram gave %v, want EOF", err)
	}
}

// A datagram larger than the header can describe is refused rather than
// truncated: a short write here would desynchronise the stream and every
// datagram after it would be read from the wrong offset.
func TestOversizedDatagramIsRefused(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDatagram(&buf, make([]byte, MaxDatagram+1)); err == nil {
		t.Error("a datagram too large for the framing was accepted")
	}
	if buf.Len() != 0 {
		t.Error("a refused datagram still wrote bytes into the stream")
	}
}

// A read buffer too small for the datagram is an error, not a truncation, for
// the same reason.
func TestShortReadBufferIsAnError(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDatagram(&buf, bytes.Repeat([]byte("y"), 500)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDatagram(&buf, make([]byte, 100)); err == nil {
		t.Error("a datagram was silently truncated into a short buffer")
	}
}

// iotest hands over one byte per Read.
type iotest struct{ r io.Reader }

func (t *iotest) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return t.r.Read(p[:1])
}

func TestAdaptiveDatagramsPreserveEverySizeAndFrame(t *testing.T) {
	sizes := []int{0, 1, 1200, 8192, 16384, 16385, MaxDatagram, 0, 7}
	var wire bytes.Buffer
	for _, size := range sizes {
		if err := WriteDatagram(&wire, bytes.Repeat([]byte{byte(size)}, size)); err != nil {
			t.Fatal(err)
		}
	}
	r := &iotest{r: &wire}
	var buf []byte
	for _, size := range sizes {
		var err error
		buf, err = ReadDatagramInto(r, buf)
		if err != nil || !bytes.Equal(buf, bytes.Repeat([]byte{byte(size)}, size)) {
			t.Fatalf("payload size %d: got %d bytes, %v", size, len(buf), err)
		}
		if cap(buf) > MaxDatagram {
			t.Fatalf("receive buffer exceeds the wire limit: %d", cap(buf))
		}
	}
	for _, truncated := range [][]byte{{1}, {0, 5, 1, 2}} {
		got, err := ReadDatagramInto(bytes.NewReader(truncated), buf)
		if len(got) != 0 || err != io.ErrUnexpectedEOF {
			t.Fatalf("incomplete frame returned %d bytes and %v", len(got), err)
		}
	}
}

func TestDatagramReceiveBuffersDoNotAllocateAfterWarmup(t *testing.T) {
	var wire bytes.Buffer
	if err := WriteDatagram(&wire, bytes.Repeat([]byte{7}, 1200)); err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(wire.Bytes())
	buf, err := ReadDatagramInto(r, nil)
	if err != nil || cap(buf) != 2048 {
		t.Fatalf("MTU-sized flow retained %d bytes, error %v", cap(buf), err)
	}
	for _, adaptive := range []bool{false, true} {
		allocs := testing.AllocsPerRun(1000, func() {
			r.Reset(wire.Bytes())
			if adaptive {
				buf, err = ReadDatagramInto(r, buf)
			} else {
				_, err = ReadDatagram(r, buf)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
		if allocs != 0 {
			t.Fatalf("adaptive=%t: %.1f allocations per warmed frame", adaptive, allocs)
		}
	}
}

func TestAdaptiveDatagramsKeepConnectionPacing(t *testing.T) {
	var wire bytes.Buffer
	for i := 0; i < 2; i++ {
		if err := WriteDatagram(&wire, bytes.Repeat([]byte{9}, 65000)); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn := limits.New(limits.Config{BandwidthMbps: 1}).Wrap(ctx, &datagramMemoryConn{Reader: bytes.NewReader(wire.Bytes())})
	start := time.Now()
	var buf []byte
	for i := 0; i < 2; i++ {
		var err error
		buf, err = ReadDatagramInto(conn, buf)
		if err != nil || len(buf) != 65000 || buf[0] != 9 || buf[len(buf)-1] != 9 {
			t.Fatalf("paced frame length=%d, error=%v", len(buf), err)
		}
	}
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("adaptive framing bypassed the connection's bandwidth limit")
	}
}

type datagramMemoryConn struct {
	net.Conn
	*bytes.Reader
}

func (c *datagramMemoryConn) Read(p []byte) (int, error) { return c.Reader.Read(p) }

func BenchmarkDatagramReceiveBuffer(b *testing.B) {
	var wire bytes.Buffer
	if err := WriteDatagram(&wire, bytes.Repeat([]byte{1}, 1200)); err != nil {
		b.Fatal(err)
	}
	b.Run("new-flow", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			r := bytes.NewReader(wire.Bytes())
			buf, err := ReadDatagramInto(r, nil)
			if err != nil || len(buf) != 1200 {
				b.Fatal(err)
			}
		}
	})
	b.Run("warmed-flow", func(b *testing.B) {
		r := bytes.NewReader(wire.Bytes())
		buf := make([]byte, 2048)
		b.ReportAllocs()
		b.SetBytes(1200)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r.Reset(wire.Bytes())
			var err error
			buf, err = ReadDatagramInto(r, buf)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
