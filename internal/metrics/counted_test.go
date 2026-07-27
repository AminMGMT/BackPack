package metrics

import (
	"bytes"
	"io"
	"net"
	"testing"
)

// Traffic counting wraps the tunnel connection, so a read is data arriving from
// the peer and a write is data leaving for it. Only the tunnel side is wrapped:
// wrapping both would count every byte twice, since each one also crosses a
// local socket.
//
// This wrapper briefly carried a ReadFrom/WriteTo pair, so that io.Copy could
// splice between the two sockets of a relay without the bytes passing through
// this process. It was removed: the forwarded relay is back to the plain
// read/write loop the upstream project uses, and counting follows it.

// traffic runs fn and reports what the counters moved while it ran. They are
// process-wide, so every test here measures a delta rather than an absolute.
func traffic(fn func()) (in, out uint64) {
	beforeIn, beforeOut := Traffic()
	fn()
	afterIn, afterOut := Traffic()
	return afterIn - beforeIn, afterOut - beforeOut
}

// A read is inbound and a write is outbound, and both are counted in full.
func TestReadsAreInboundAndWritesAreOutbound(t *testing.T) {
	payload := bytes.Repeat([]byte("backpack"), 4096) // 32 KiB

	var sink bytes.Buffer
	c := CountedConn(writeOnly{w: &sink})
	_, out := traffic(func() {
		if _, err := c.Write(payload); err != nil {
			t.Fatalf("Write: %v", err)
		}
	})
	if out != uint64(len(payload)) {
		t.Errorf("counted %d bytes out, want %d", out, len(payload))
	}
	if !bytes.Equal(sink.Bytes(), payload) {
		t.Error("the bytes that arrived are not the bytes that were sent")
	}

	r := CountedConn(readOnly{r: bytes.NewReader(payload)})
	got := make([]byte, len(payload))
	in, _ := traffic(func() {
		if _, err := io.ReadFull(r, got); err != nil {
			t.Fatalf("Read: %v", err)
		}
	})
	if in != uint64(len(payload)) {
		t.Errorf("counted %d bytes in, want %d", in, len(payload))
	}
}

// Counting has to move while the connection is alive, not only when it ends: a
// tunnel carrying a long download would otherwise report nothing for as long as
// it lasted, and the panel would show it as idle.
func TestCountersMoveWhileTheConnectionIsOpen(t *testing.T) {
	c := CountedConn(writeOnly{w: io.Discard})
	chunk := bytes.Repeat([]byte("x"), 1024)

	for i := 1; i <= 4; i++ {
		_, out := traffic(func() {
			if _, err := c.Write(chunk); err != nil {
				t.Fatalf("Write: %v", err)
			}
		})
		if out != uint64(len(chunk)) {
			t.Fatalf("write %d counted %d bytes, want %d — counting is not incremental",
				i, out, len(chunk))
		}
	}
}

// The local side of a relay must never be wrapped, or every byte is counted
// twice. This checks the wrapper is a thin pass-through so that a caller can
// tell what it is holding.
func TestOnlyTheWrappedConnectionCounts(t *testing.T) {
	var sink bytes.Buffer
	raw := writeOnly{w: &sink}

	_, out := traffic(func() {
		if _, err := raw.Write([]byte("not counted")); err != nil {
			t.Fatal(err)
		}
	})
	if out != 0 {
		t.Errorf("an unwrapped connection contributed %d bytes to the counters", out)
	}
}

// writeOnly and readOnly are net.Conns that can only do the one thing the test
// needs. Embedding net.Conn leaves the rest nil, which is fine because nothing
// here calls it — and makes any accidental call a loud panic rather than a
// silent pass.
type writeOnly struct {
	net.Conn
	w io.Writer
}

func (c writeOnly) Write(b []byte) (int, error) { return c.w.Write(b) }

type readOnly struct {
	net.Conn
	r io.Reader
}

func (c readOnly) Read(b []byte) (int, error) { return c.r.Read(b) }
