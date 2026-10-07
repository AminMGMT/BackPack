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

			// Finishing both peers must release the whole relay.
			client.Close()
			backend.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the relay did not finish after both ends closed")
			}
		})
	}
}

// A sender's EOF leaves the response direction open. Once both directions
// finish, the handler joins both copies and closes the sockets.
func TestRelayClosesBothEnds(t *testing.T) {
	client, from := tcpPair(t)
	to, backend := tcpPair(t)
	defer client.Close()
	defer backend.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		TCPConnectionHandler(ctx, false, from, to, quietLogger(), &web.Usage{}, 8080, false)
	}()
	request := []byte("request finished by EOF")
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	if err := client.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	backend.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := io.ReadAll(backend)
	if err != nil || !bytes.Equal(got, request) {
		t.Fatalf("EOF did not preserve the request: %q, %v", got, err)
	}
	select {
	case <-done:
		t.Fatal("sender EOF closed the response direction")
	default:
	}
	response := []byte("remaining response")
	if _, err := backend.Write(response); err != nil {
		t.Fatal(err)
	}
	if err := backend.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err = io.ReadAll(client)
	if err != nil || !bytes.Equal(got, response) {
		t.Fatalf("half-close response: %q, %v", got, err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay did not join both finished copies")
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
