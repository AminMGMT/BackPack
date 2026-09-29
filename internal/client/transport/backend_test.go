package transport

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/utils"
	"github.com/xtaci/smux"
)

// echoService is the local service a user asked for: it echoes what it reads.
func echoService(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

// A mux session as the client serves it: the server side opens streams and
// names a target on each, the client relays each to its backend.
func TestEachStreamOnASessionReachesTheBackendItNames(t *testing.T) {
	target := echoService(t)
	serverEnd, clientEnd := net.Pipe()
	defer serverEnd.Close()

	var l lifecycle
	l.firstGeneration(context.Background(), silentLogger(), usageSpec{})

	clientSession, err := smux.Server(clientEnd, smux.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	go l.serveSession(clientSession, clientEnd.RemoteAddr(), backendOpts{dialTimeout: time.Second, keepAlive: time.Second})

	serverSession, err := smux.Client(serverEnd, smux.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}

	// A stream that never names its target must not hold up the ones behind
	// it: the target is read in the stream's own goroutine.
	silent, err := serverSession.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()

	for i := 0; i < 3; i++ {
		s, err := serverSession.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		if err := utils.SendBinaryString(s, target); err != nil {
			t.Fatal(err)
		}
		msg := []byte("hello")
		if _, err := s.Write(msg); err != nil {
			t.Fatal(err)
		}
		_ = s.SetReadDeadline(time.Now().Add(3 * time.Second))
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(s, got); err != nil {
			t.Fatalf("stream %d: nothing came back from the backend: %v", i, err)
		}
		if string(got) != "hello" {
			t.Fatalf("stream %d: got %q", i, got)
		}
		s.Close()
	}
}

// A backend that refuses is the user's connection closed, not a hang.
func TestARefusedBackendClosesTheUsersConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	ln.Close()

	var l lifecycle
	l.firstGeneration(context.Background(), silentLogger(), usageSpec{})
	user, tunnel := net.Pipe()
	done := make(chan struct{})
	go func() { l.relayStream(tunnel, dead, backendOpts{dialTimeout: time.Second}); close(done) }()

	_ = user.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := user.Read(make([]byte, 1)); err == nil {
		t.Fatal("the user's connection stayed open with nothing behind it")
	}
	<-done
}

// A session belongs to its generation: when the generation ends, the session
// is closed and its loop returns. Over KCP nothing else would end it — the
// churn test found thirty-two of them left behind by eight clients.
func TestASessionEndsWithItsGeneration(t *testing.T) {
	serverEnd, clientEnd := net.Pipe()
	defer serverEnd.Close()

	parent, end := context.WithCancel(context.Background())
	var l lifecycle
	l.firstGeneration(parent, silentLogger(), usageSpec{})

	session, err := smux.Server(clientEnd, smux.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := smux.Client(serverEnd, smux.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { l.serveSession(session, clientEnd.RemoteAddr(), backendOpts{}); close(done) }()

	end()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the session outlived its generation")
	}
	if !session.IsClosed() {
		t.Fatal("the loop returned but the session is still open")
	}
}
