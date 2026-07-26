package metrics

import (
	"bytes"
	"io"
	"net"
	"testing"
)

// The counting wrapper sits on the tunnel connection, which means it also sits
// on the path io.Copy uses to reach a file descriptor and splice. A wrapper
// offering only Read and Write hides that descriptor, so counting a connection
// used to quietly cost it the kernel's zero-copy path on every byte it carried.
// These tests pin both halves of the fix: the fast path is reachable, and the
// numbers it reports are still exact.

// A wrapper that exposes neither method sends io.Copy down the buffered path
// regardless of what it wraps, so this is the property the whole change rests
// on.
func TestCountedConnDoesNotHideTheFastPath(t *testing.T) {
	c := CountedConn(&net.TCPConn{})
	if _, ok := c.(io.ReaderFrom); !ok {
		t.Error("a counted connection must offer ReadFrom, or io.Copy cannot splice into it")
	}
	if _, ok := c.(io.WriterTo); !ok {
		t.Error("a counted connection must offer WriteTo, or io.Copy cannot splice out of it")
	}
}

// traffic runs fn and reports what the tunnel counters moved while it ran. The
// counters are process-wide, so every test here measures a delta rather than an
// absolute.
func traffic(fn func()) (in, out uint64) {
	beforeIn, beforeOut := Traffic()
	fn()
	afterIn, afterOut := Traffic()
	return afterIn - beforeIn, afterOut - beforeOut
}

// ReadFrom feeds the tunnel, so what it moves is outbound — and all of it must
// be counted even though this process never sees the bytes.
func TestReadFromCountsEveryByteAsOutbound(t *testing.T) {
	payload := bytes.Repeat([]byte("backpack"), 4096) // 32 KiB, more than one copy buffer
	var sink bytes.Buffer
	c := CountedConn(writeOnly{w: &sink}).(*countedConn)

	in, out := traffic(func() {
		n, err := c.ReadFrom(bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("ReadFrom: %v", err)
		}
		if n != int64(len(payload)) {
			t.Fatalf("ReadFrom moved %d bytes, want %d", n, len(payload))
		}
	})

	if out != uint64(len(payload)) {
		t.Errorf("counted %d bytes out, want %d", out, len(payload))
	}
	if in != 0 {
		t.Errorf("counted %d bytes in, want 0 — ReadFrom is outbound only", in)
	}
	if !bytes.Equal(sink.Bytes(), payload) {
		t.Error("the bytes that arrived are not the bytes that were sent")
	}
}

// WriteTo drains the tunnel, so what it moves is inbound.
func TestWriteToCountsEveryByteAsInbound(t *testing.T) {
	payload := bytes.Repeat([]byte("backpack"), 4096)
	c := CountedConn(readOnly{r: bytes.NewReader(payload)}).(*countedConn)

	var sink bytes.Buffer
	in, out := traffic(func() {
		n, err := c.WriteTo(&sink)
		if err != nil {
			t.Fatalf("WriteTo: %v", err)
		}
		if n != int64(len(payload)) {
			t.Fatalf("WriteTo moved %d bytes, want %d", n, len(payload))
		}
	})

	if in != uint64(len(payload)) {
		t.Errorf("counted %d bytes in, want %d", in, len(payload))
	}
	if out != 0 {
		t.Errorf("counted %d bytes out, want 0 — WriteTo is inbound only", out)
	}
}

// The fast path must not count a byte twice. It would if ReadFrom reached the
// wrapper's own Write instead of the connection underneath it, which is also
// the shape that would recurse forever.
func TestFastPathCountsBytesOnlyOnce(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 8192)

	var viaWrite, viaReadFrom uint64
	c := CountedConn(writeOnly{w: io.Discard}).(*countedConn)
	_, viaWrite = traffic(func() {
		if _, err := c.Write(payload); err != nil {
			t.Fatalf("Write: %v", err)
		}
	})
	_, viaReadFrom = traffic(func() {
		if _, err := c.ReadFrom(bytes.NewReader(payload)); err != nil {
			t.Fatalf("ReadFrom: %v", err)
		}
	})

	if viaWrite != viaReadFrom {
		t.Errorf("the same payload counted as %d bytes written and %d bytes spliced; "+
			"the two paths must agree", viaWrite, viaReadFrom)
	}
}

// End to end over real sockets: a counted tunnel connection relayed with
// io.Copy must deliver the payload intact and report it exactly once. This is
// the arrangement the transports actually build.
func TestCountedRelayOverRealSocketsIsExact(t *testing.T) {
	payload := bytes.Repeat([]byte("through-the-tunnel"), 2048)

	client, server := tcpPair(t)
	sink, sinkPeer := tcpPair(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// The tunnel side is the counted one, exactly as the transports wrap it.
		io.Copy(sinkPeer, CountedConn(server))
		sinkPeer.Close()
	}()

	in, _ := traffic(func() {
		go func() {
			client.Write(payload)
			client.Close()
		}()
		got, err := io.ReadAll(sink)
		if err != nil {
			t.Errorf("reading the relayed payload: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("relayed %d bytes, want %d — the payload did not survive", len(got), len(payload))
		}
		<-done
	})

	if in != uint64(len(payload)) {
		t.Errorf("counted %d bytes in, want %d", in, len(payload))
	}
}

// tcpPair returns the two ends of a connected TCP connection.
func tcpPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	type accepted struct {
		conn net.Conn
		err  error
	}
	ch := make(chan accepted, 1)
	go func() {
		c, err := ln.Accept()
		ch <- accepted{c, err}
	}()

	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	a := <-ch
	if a.err != nil {
		t.Fatalf("accept: %v", a.err)
	}
	t.Cleanup(func() { client.Close(); a.conn.Close() })
	return client, a.conn
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
