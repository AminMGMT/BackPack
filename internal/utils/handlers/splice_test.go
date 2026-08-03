package handlers

import (
	"net"
	"testing"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/sirupsen/logrus"
)

// The fast path must decline anything that is not two plain TCP sockets, on
// every platform. A mux stream, a KCP session, a websocket or a rate-limited
// connection all arrive here as something other than *net.TCPConn, and the
// caller relies on a false return to copy them itself.
func TestSpliceTransferDeclinesNonTCPConns(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	if spliceTransfer(a, b, logger, nil, 0, false) {
		t.Fatal("the zero-copy path accepted connections it cannot splice")
	}
}

// Unwrapping the counter is what lets the fast path reach the real socket, and
// the second return value is what tells it which side is the tunnel — so that
// the bytes it moves are counted in the right direction. Getting this wrong
// would either lose the tunnel's traffic figures or count them backwards.
func TestUncountIdentifiesTheTunnelSide(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	if inner, counted := metrics.Uncount(a); counted || inner != a {
		t.Fatal("a plain connection was reported as the counted side")
	}

	wrapped := metrics.CountedConn(a)
	inner, counted := metrics.Uncount(wrapped)
	if !counted {
		t.Fatal("a counted connection was not recognised as one")
	}
	if inner != a {
		t.Fatal("unwrapping did not return the underlying connection")
	}
}

// A rate-limited connection must never reach the fast path: pacing bytes means
// looking at them, and the kernel would move them past the limiter entirely.
// The guarantee comes from not unwrapping anything except the counter, so what
// this checks is that an unknown wrapper is declined rather than unwrapped.
func TestSpliceTransferDeclinesAnUnknownWrapper(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	done := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		done <- c
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	server := <-done
	defer server.Close()

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Both ends are real TCP sockets, but one is behind a wrapper the fast
	// path does not know how to account for.
	if spliceTransfer(opaqueWrapper{client}, server, logger, nil, 0, false) {
		t.Fatal("the zero-copy path unwrapped a connection it does not understand")
	}
}

// opaqueWrapper stands in for any wrapper that changes what a read or a write
// means — the bandwidth limiter being the one that actually exists.
type opaqueWrapper struct{ net.Conn }
