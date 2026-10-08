package direct

import (
	"context"
	"errors"
	"net"
	"runtime"
	"testing"
	"time"
)

// A websocket session that has ended leaves nothing behind in the origin.
//
// The upgrade handler handed each connection to the session loop and then
// waited for the listener to close — to stop the HTTP server closing the
// connection, which it never does once the connection has been hijacked. So
// every session the origin ever served kept a goroutine until the tunnel
// stopped: on an origin that runs for months, one per connection ever made.
func TestAnEndedWebsocketSessionLeavesNoGoroutine(t *testing.T) {
	cfg := &Config{Role: RoleOrigin, Addr: "127.0.0.1:0", Token: "a-long-enough-token", Transport: TransportWS}
	l, err := listenWebSocket(cfg, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	dialCfg := *cfg
	dialCfg.Addr = l.listener.Addr().String()
	dialCfg.DialTimeout = 2 * time.Second

	session := func() {
		c, err := dialWebSocket(context.Background(), &dialCfg)
		if err != nil {
			t.Fatal(err)
		}
		s, err := l.Accept()
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
		c.Close()
	}
	session()
	settle := func() int {
		time.Sleep(200 * time.Millisecond)
		runtime.GC()
		return runtime.NumGoroutine()
	}
	base := settle()
	for i := 0; i < 20; i++ {
		session()
	}
	if after := settle(); after > base+5 {
		t.Fatalf("goroutines grew from %d to %d over twenty ended sessions", base, after)
	}
}

// The HTTP server can stop independently of the wrapper. Its exit must wake
// Accept so the origin can retire this generation and bind a replacement.
func TestWebsocketServerExitUnblocksAccept(t *testing.T) {
	cfg := &Config{Role: RoleOrigin, Addr: "127.0.0.1:0", Token: "a-long-enough-token", Transport: TransportWS}
	l, err := listenWebSocket(cfg, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if conn != nil {
			conn.Close()
		}
		done <- err
	}()
	if err := l.listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept error = %v, want closed listener", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP server exited but websocket Accept kept waiting")
	}
	select {
	case <-l.closed:
	default:
		t.Fatal("server exit did not close pending upgrade handlers")
	}
}
