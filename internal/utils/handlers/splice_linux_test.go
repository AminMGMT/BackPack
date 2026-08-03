//go:build linux

package handlers

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// splicePair returns a connected pair of real TCP sockets.
func splicePair(t *testing.T) (client, server *net.TCPConn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	s := <-accepted
	if s == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() { c.Close(); s.Close() })
	return c.(*net.TCPConn), s.(*net.TCPConn)
}

// Everything that goes in must come out, byte for byte and in order. A relay
// that corrupts or reorders is worse than a slow one.
func TestSpliceRelayMovesEverythingIntact(t *testing.T) {
	// Two pairs wired as: writer -> src ... dst -> reader.
	writer, src := splicePair(t)
	dst, reader := splicePair(t)

	payload := make([]byte, 4<<20) // 4 MiB, several times the pipe
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}

	var counted atomic.Int64
	relayDone := make(chan error, 1)
	go func() {
		handled, err := spliceRelay(dst, src, func(n int) { counted.Add(int64(n)) })
		if !handled {
			t.Error("splice declined two plain TCP sockets")
		}
		relayDone <- err
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := writer.Write(payload); err != nil {
			t.Errorf("write: %v", err)
		}
		writer.CloseWrite()
	}()

	got := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	wg.Wait()

	select {
	case err := <-relayDone:
		if err != nil {
			t.Fatalf("relay ended with %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the relay never finished")
	}

	if !bytes.Equal(got, payload) {
		t.Fatal("the bytes that arrived are not the bytes that were sent")
	}
	if n := counted.Load(); n != int64(len(payload)) {
		t.Fatalf("accounted for %d bytes, %d were moved", n, len(payload))
	}
}

// Accounting has to move while the transfer is running, not only when it ends.
// This is the whole reason the loop is written out here instead of being left
// to io.Copy: a running download must not show up as an idle tunnel.
func TestSpliceRelayAccountsWhileTheTransferRuns(t *testing.T) {
	writer, src := splicePair(t)
	dst, reader := splicePair(t)

	var counted atomic.Int64
	go func() { _, _ = spliceRelay(dst, src, func(n int) { counted.Add(int64(n)) }) }()

	// Drain the far end so the transfer can keep moving.
	go func() { _, _ = io.Copy(io.Discard, reader) }()

	chunk := bytes.Repeat([]byte("x"), 32<<10)
	if _, err := writer.Write(chunk); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if counted.Load() > 0 {
			// The connection is still open and bytes are already accounted for,
			// which is what was being checked.
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no bytes were accounted for while the transfer was still open")
}

// A relay ends when the far side stops sending, and must report that as a clean
// finish rather than an error — otherwise every normal connection close would
// be logged as a failure.
func TestSpliceRelayEndsCleanlyOnEOF(t *testing.T) {
	writer, src := splicePair(t)
	dst, reader := splicePair(t)
	go func() { _, _ = io.Copy(io.Discard, reader) }()

	done := make(chan error, 1)
	go func() {
		_, err := spliceRelay(dst, src, func(int) {})
		done <- err
	}()

	if _, err := writer.Write([]byte("bye")); err != nil {
		t.Fatalf("write: %v", err)
	}
	writer.CloseWrite()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a clean end was reported as %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the relay did not notice the far side finishing")
	}
}
