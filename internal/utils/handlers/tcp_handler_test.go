package handlers

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/web"
	"github.com/sirupsen/logrus"
)

// The forwarded relay: a read/write loop per direction that closes both ends
// when either finishes. It briefly had a second, zero-copy path; that was
// removed to keep this identical to the upstream project, whose behaviour is
// the one proven on real tunnels. These tests cover what the loop must do
// regardless — carry both directions intact, and never leave one end open.

func quietLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

// tcpPair returns the two ends of a connected TCP connection. Real sockets, not
// net.Pipe, because the fast path only exists between real file descriptors.
func tcpPair(t *testing.T) (a, b net.Conn) {
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

	a, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	got := <-ch
	if got.err != nil {
		t.Fatalf("accept: %v", got.err)
	}
	t.Cleanup(func() { a.Close(); got.conn.Close() })
	return a, got.conn
}

// A forwarded connection is full duplex, and the sniffer setting must not
// change a single byte of what crosses it.
func TestRelayCarriesBothDirections(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sniffer bool
	}{
		{"plain", false},
		{"sniffer", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, from := tcpPair(t)
			to, backend := tcpPair(t)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan struct{})
			go func() {
				defer close(done)
				TCPConnectionHandler(ctx, false, from, to, quietLogger(), &web.Usage{}, 8080, tc.sniffer)
			}()

			// Big enough to cross more than one copy buffer in either path.
			upstream := bytes.Repeat([]byte("client-to-backend"), 3000)
			downstream := bytes.Repeat([]byte("backend-to-client"), 3000)

			go func() { client.Write(upstream); backend.Write(downstream) }()

			assertReceives(t, backend, upstream, "client to backend")
			assertReceives(t, client, downstream, "backend to client")

			// Closing one end must bring the whole relay down.
			client.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the relay did not finish after one end closed")
			}
		})
	}
}

// When either side goes away the handler closes both connections, so a forwarded
// connection can never be left half open holding a socket open forever.
func TestRelayClosesBothEnds(t *testing.T) {
	client, from := tcpPair(t)
	to, backend := tcpPair(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		TCPConnectionHandler(ctx, false, from, to, quietLogger(), &web.Usage{}, 8080, false)
	}()

	client.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay did not finish after the client closed")
	}

	// The far end must see the connection end rather than hang.
	backend.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := backend.Read(make([]byte, 1)); err == nil {
		t.Error("the backend side stayed open after the client closed")
	}
}

// Cancellation is the shutdown path used by transport restarts and config
// reloads. It must interrupt two idle Reads; waiting for traffic or for a peer
// to close would leave the previous generation alive indefinitely.
func TestRelayCancellationClosesBothEnds(t *testing.T) {
	client, from := tcpPair(t)
	to, backend := tcpPair(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		TCPConnectionHandler(ctx, false, from, to, quietLogger(), &web.Usage{}, 8080, false)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("an idle relay ignored context cancellation")
	}

	for name, conn := range map[string]net.Conn{"client": client, "backend": backend} {
		conn.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := conn.Read(make([]byte, 1)); err == nil {
			t.Errorf("%s side stayed open after cancellation", name)
		}
	}
}

// assertReceives reads exactly len(want) bytes from c and compares them.
func assertReceives(t *testing.T, c net.Conn, want []byte, direction string) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	got := make([]byte, len(want))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("%s: reading the relayed payload: %v", direction, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: the payload did not survive the relay", direction)
	}
	c.SetReadDeadline(time.Time{})
}
