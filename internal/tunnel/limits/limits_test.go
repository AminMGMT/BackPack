package limits

import (
	"net"
	"sync"
	"testing"
	"time"
)

// A tunnel that asked for nothing must get a nil limiter, so the unlimited
// path stays free.
func TestUnlimitedIsNil(t *testing.T) {
	if l := New(Config{}); l != nil {
		t.Fatal("an unlimited config produced a limiter")
	}
	var nilLimiter *Limiter
	if !nilLimiter.Acquire() {
		t.Fatal("a nil limiter refused a connection")
	}
	nilLimiter.Release() // must not panic
	if nilLimiter.Active() != 0 {
		t.Fatal("a nil limiter reported active connections")
	}
	conn, _ := net.Pipe()
	defer conn.Close()
	if nilLimiter.Wrap(conn) != conn {
		t.Fatal("a nil limiter wrapped a connection")
	}
}

func TestConnectionCap(t *testing.T) {
	l := New(Config{MaxConnections: 3})

	for i := 0; i < 3; i++ {
		if !l.Acquire() {
			t.Fatalf("slot %d was refused while under the cap", i)
		}
	}
	if l.Acquire() {
		t.Fatal("a fourth connection was admitted past a cap of three")
	}
	if l.Active() != 3 {
		t.Fatalf("Active = %d, want 3", l.Active())
	}

	l.Release()
	if !l.Acquire() {
		t.Fatal("a released slot was not reusable")
	}
}

// A refused Acquire must not leave a slot consumed, or the cap would erode
// with every refusal until nothing could connect at all.
func TestRefusalDoesNotLeakASlot(t *testing.T) {
	l := New(Config{MaxConnections: 1})
	if !l.Acquire() {
		t.Fatal("the first connection was refused")
	}
	for i := 0; i < 100; i++ {
		if l.Acquire() {
			t.Fatal("a second connection was admitted past a cap of one")
		}
	}
	l.Release()
	if !l.Acquire() {
		t.Fatal("the cap eroded: the slot could not be taken after 100 refusals")
	}
}

func TestConnectionCapUnderConcurrency(t *testing.T) {
	const cap = 10
	l := New(Config{MaxConnections: cap})

	var wg sync.WaitGroup
	admitted := make(chan struct{}, 200)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Acquire() {
				admitted <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(admitted)

	if got := len(admitted); got != cap {
		t.Fatalf("%d connections were admitted past a cap of %d", got, cap)
	}
}

// Bandwidth alone must still produce a limiter, and must not cap connections.
func TestBandwidthOnly(t *testing.T) {
	l := New(Config{BandwidthMbps: 10})
	if l == nil {
		t.Fatal("a bandwidth-only config produced no limiter")
	}
	for i := 0; i < 1000; i++ {
		if !l.Acquire() {
			t.Fatal("a bandwidth-only limiter capped connections")
		}
	}
}

// The cap has to actually slow a sustained transfer down.
//
// The first burst is deliberately free — a limited tunnel should start moving
// immediately rather than trickling from the first byte — so the burst is spent
// first and only what follows is timed. Measuring the burst instead is what
// makes a working cap look broken.
func TestBandwidthCapPacesSustainedTransfer(t *testing.T) {
	// 8 Mbit/s is one megabyte a second, so the burst is one megabyte.
	l := New(Config{BandwidthMbps: 8})

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		buf := make([]byte, 64*1024)
		for {
			if _, err := server.Read(buf); err != nil {
				return
			}
		}
	}()

	wrapped := l.Wrap(client)

	// Spend the burst. This part is expected to be quick.
	if _, err := wrapped.Write(make([]byte, 1024*1024)); err != nil {
		t.Fatalf("write (burst): %v", err)
	}

	// Now half a megabyte at a megabyte a second: about half a second.
	start := time.Now()
	if _, err := wrapped.Write(make([]byte, 512*1024)); err != nil {
		t.Fatalf("write (sustained): %v", err)
	}
	elapsed := time.Since(start)

	// A generous floor: this measures wall-clock time on a shared machine, and
	// the point is only that pacing happens at all.
	if elapsed < 250*time.Millisecond {
		t.Fatalf("half a megabyte past the burst of an 8 Mbit/s cap took %s — the cap is not being applied", elapsed)
	}
}

// An uncapped connection must not be paced at all.
func TestNoBandwidthCapDoesNotPace(t *testing.T) {
	l := New(Config{MaxConnections: 5}) // connections only

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		buf := make([]byte, 64*1024)
		for {
			if _, err := server.Read(buf); err != nil {
				return
			}
		}
	}()

	wrapped := l.Wrap(client)
	if wrapped != client {
		t.Fatal("a connection was wrapped despite no bandwidth cap")
	}

	start := time.Now()
	if _, err := wrapped.Write(make([]byte, 4*1024*1024)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("an uncapped write took %s", elapsed)
	}
}

// A single write larger than one second's worth must still complete. Charged
// in bucket-sized pieces; without that it would fail outright forever.
func TestWriteLargerThanTheBucket(t *testing.T) {
	l := New(Config{BandwidthMbps: 8}) // a 1 MB bucket

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		buf := make([]byte, 64*1024)
		for {
			if _, err := server.Read(buf); err != nil {
				return
			}
		}
	}()

	wrapped := l.Wrap(client)
	done := make(chan error, 1)
	go func() {
		_, err := wrapped.Write(make([]byte, 3*1024*1024)) // three buckets
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("an oversize write failed: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("an oversize write never completed")
	}
}
