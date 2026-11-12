package paqet

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/xtaci/smux"
)

func streamPair(t *testing.T) (client, server *smux.Stream) {
	t.Helper()
	a, b := net.Pipe()
	cs, err := smux.Client(a, smux.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	ss, err := smux.Server(b, smux.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); ss.Close() })
	client, err = cs.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	client.Write([]byte{0xFF}) // a stream is not announced until it carries something
	server, err = ss.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(server, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	return client, server
}

// A frame whose header arrives in two pieces across a read deadline is still one
// frame: the byte read before the deadline is kept, not lost.
func TestAReadDeadlineInsideAFrameHeaderLosesNothing(t *testing.T) {
	cli, srv := streamPair(t)
	r := newFramedStream(srv)

	cli.Write([]byte{0x00}) // the first byte of a two-byte length
	r.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	var ne net.Error
	if _, err := r.Read(make([]byte, 8)); !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("first read: %v, want a deadline", err)
	}
	r.SetReadDeadline(time.Time{})
	cli.Write([]byte{0x03, 'a', 'b', 'c', 0, 0}) // the rest of the length, 3 bytes, then the end marker
	buf := make([]byte, 8)
	n, err := r.Read(buf)
	if err != nil || string(buf[:n]) != "abc" {
		t.Fatalf("read %q, %v; want abc", buf[:n], err)
	}
	if _, err := r.Read(buf); err != io.EOF {
		t.Fatalf("after the marker: %v, want EOF", err)
	}
	if _, err := r.Read(buf); err != io.EOF {
		t.Fatalf("EOF is not repeated: %v", err)
	}
}

// A stream closed whole, with no marker, reads as an end too; closed in the
// middle of a frame it is reported as the truncation it is.
func TestAStreamClosedWithoutTheMarkerEndsAndATruncatedFrameIsReported(t *testing.T) {
	cli, srv := streamPair(t)
	r := newFramedStream(srv)
	cli.Close()
	if _, err := r.Read(make([]byte, 4)); err != io.EOF {
		t.Fatalf("a whole close: %v, want EOF", err)
	}

	cli, srv = streamPair(t)
	r = newFramedStream(srv)
	cli.Write([]byte{0x00, 0x05, 'a', 'b'}) // five bytes promised, two sent
	cli.Close()
	buf := make([]byte, 8)
	n, err := r.Read(buf)
	if err != nil || string(buf[:n]) != "ab" {
		t.Fatalf("read %q, %v", buf[:n], err)
	}
	if _, err := r.Read(buf); err != io.ErrUnexpectedEOF {
		t.Fatalf("a frame cut short: %v, want ErrUnexpectedEOF", err)
	}
}

// Writing after CloseWrite is refused, and CloseWrite twice is one marker.
func TestCloseWriteIsOnceAndForAll(t *testing.T) {
	cli, srv := streamPair(t)
	w, r := newFramedStream(cli), newFramedStream(srv)
	w.Write([]byte("hi"))
	if err := w.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if err := w.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("late")); !errors.Is(err, errWriteClosed) {
		t.Fatalf("write after CloseWrite: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil || string(got) != "hi" {
		t.Fatalf("read %q, %v", got, err)
	}
	// The read side of the closed end still works: the peer can answer.
	r.Write([]byte("reply"))
	buf := make([]byte, 8)
	n, err := w.Read(buf)
	if err != nil || string(buf[:n]) != "reply" {
		t.Fatalf("the reply to a half-closed end: %q, %v", buf[:n], err)
	}
}
