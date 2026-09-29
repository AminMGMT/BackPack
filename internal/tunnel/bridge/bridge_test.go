package bridge

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func tcpPair(t *testing.T) (a, b net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); got <- c }()
	a, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b = <-got
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

// What one side sends reaches the other, in both directions, and a side that
// has finished sending is told so by an orderly end rather than a timeout.
func TestJoinCarriesBothWaysAndPassesTheEnd(t *testing.T) {
	user, userEnd := tcpPair(t)
	backendEnd, backend := tcpPair(t)
	done := make(chan struct{})
	go func() { Join(context.Background(), userEnd, backendEnd); close(done) }()

	if _, err := user.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	_ = backend.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(backend, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("backend read %q, %v", buf, err)
	}
	if _, err := backend.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	_ = user.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(user, buf); err != nil || string(buf) != "pong" {
		t.Fatalf("user read %q, %v", buf, err)
	}

	user.(*net.TCPConn).CloseWrite()
	_ = backend.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := backend.Read(buf); err != io.EOF {
		t.Fatalf("the user's end did not reach the backend as an end: %v", err)
	}
	backend.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Join did not return once both sides were done")
	}
}

// A cancelled context ends the join even when neither side has closed.
func TestJoinEndsWithItsContext(t *testing.T) {
	_, a := tcpPair(t)
	b, _ := tcpPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Join(ctx, a, b); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Join outlived its context")
	}
}
