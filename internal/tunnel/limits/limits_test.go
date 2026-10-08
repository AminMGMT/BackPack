package limits

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
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
	if nilLimiter.Wrap(context.Background(), conn) != conn {
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

	wrapped := l.Wrap(context.Background(), client)

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

	wrapped := l.Wrap(context.Background(), client)
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

	wrapped := l.Wrap(context.Background(), client)
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

// Socket deadlines also cover pacing, and updates affect the reservation already
// in progress. A direction's deadline must not interrupt the other direction.
func TestPacingHonorsMutableDeadlines(t *testing.T) {
	for _, mode := range []string{"before", "during", "move", "clear", "extend", "read", "both", "isolation"} {
		t.Run(mode, func(t *testing.T) {
			socket, peer := net.Pipe()
			defer peer.Close()
			plain := &deadlinePacingProbe{Conn: socket}
			bucket := rate.NewLimiter(1, 1)
			bucket.AllowN(time.Now(), 1)
			conn := (&Limiter{bucket: bucket}).Wrap(context.Background(), plain)
			defer conn.Close()
			if mode == "before" {
				conn.SetWriteDeadline(time.Now().Add(30 * time.Millisecond))
			}
			if mode == "move" {
				conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			}
			if mode == "clear" || mode == "extend" {
				conn.SetWriteDeadline(time.Now().Add(150 * time.Millisecond))
			}
			if mode == "read" {
				conn.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
			}
			if mode == "isolation" {
				conn.SetReadDeadline(time.Now().Add(-time.Second))
			}
			type result struct {
				n   int
				err error
			}
			done := make(chan result, 1)
			go func() {
				if mode == "read" {
					n, err := conn.Read(make([]byte, 1))
					done <- result{n, err}
				} else {
					n, err := conn.Write([]byte{7})
					done <- result{n, err}
				}
			}()
			if mode == "read" {
				if _, err := peer.Write([]byte{7}); err != nil {
					t.Fatal(err)
				}
			}
			until := time.Now().Add(time.Second)
			for bucket.Tokens() > -0.5 && time.Now().Before(until) {
				time.Sleep(time.Millisecond)
			}
			if bucket.Tokens() > -0.5 {
				t.Fatal("operation did not enter pacing")
			}
			switch mode {
			case "during", "move":
				conn.SetWriteDeadline(time.Now().Add(30 * time.Millisecond))
			case "both":
				conn.SetDeadline(time.Now().Add(30 * time.Millisecond))
			case "clear":
				conn.SetWriteDeadline(time.Time{})
			case "extend":
				conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			}
			successful := mode == "clear" || mode == "extend" || mode == "isolation"
			if successful {
				drained := make(chan struct{})
				go func() { peer.Read(make([]byte, 1)); close(drained) }()
				select {
				case r := <-done:
					if r.n != 1 || r.err != nil {
						t.Fatalf("updated deadline n%d err%v", r.n, r.err)
					}
				case <-time.After(2 * time.Second):
					conn.Close()
					t.Fatal("updated deadline did not release paced operation")
				}
				<-drained
			} else {
				select {
				case r := <-done:
					wantN := 0
					if mode == "read" {
						wantN = 1
					}
					if r.n != wantN || !errors.Is(r.err, os.ErrDeadlineExceeded) {
						t.Fatalf("deadline n%d err%v", r.n, r.err)
					}
				case <-time.After(300 * time.Millisecond):
					conn.Close()
					<-done
					t.Fatal("pacing ignored the socket deadline")
				}
				if plain.writes.Load() != 0 {
					t.Fatal("expired pacing reached the socket Write")
				}
				if tokens := bucket.Tokens(); tokens < -0.1 {
					t.Fatalf("pending reservation was not refunded: %g", tokens)
				}
			}
		})
	}
}

type deadlinePacingProbe struct {
	net.Conn
	writes  atomic.Int64
	reject  bool
	readEOF bool
}

func (c *deadlinePacingProbe) Write(p []byte) (int, error) { c.writes.Add(1); return c.Conn.Write(p) }
func (c *deadlinePacingProbe) Read(p []byte) (int, error) {
	if c.readEOF {
		p[0] = 7
		return 1, io.EOF
	}
	return c.Conn.Read(p)
}
func (c *deadlinePacingProbe) SetDeadline(t time.Time) error {
	if c.reject {
		return errPacingDeadlineRejected
	}
	return c.Conn.SetDeadline(t)
}
func (c *deadlinePacingProbe) SetReadDeadline(t time.Time) error {
	if c.reject {
		return errPacingDeadlineRejected
	}
	return c.Conn.SetReadDeadline(t)
}
func (c *deadlinePacingProbe) SetWriteDeadline(t time.Time) error {
	if c.reject {
		return errPacingDeadlineRejected
	}
	return c.Conn.SetWriteDeadline(t)
}

var errPacingDeadlineRejected = errors.New("deadline rejected")

// Failed setters leave pacing unchanged, and timeout does not discard bytes or
// replace an EOF returned by the socket. Closing still cancels a pending wait.
func TestPacingDeadlineFailureAndEOF(t *testing.T) {
	for _, mode := range []string{"setter", "EOF", "close"} {
		t.Run(mode, func(t *testing.T) {
			socket, peer := net.Pipe()
			defer peer.Close()
			plain := &deadlinePacingProbe{Conn: socket, reject: mode == "setter", readEOF: mode == "EOF"}
			bucket := rate.NewLimiter(100, 1)
			bucket.AllowN(time.Now(), 1)
			conn := (&Limiter{bucket: bucket}).Wrap(context.Background(), plain)
			defer conn.Close()
			if mode == "setter" {
				for _, set := range []func(time.Time) error{conn.SetDeadline, conn.SetReadDeadline, conn.SetWriteDeadline} {
					if !errors.Is(set(time.Now().Add(-time.Second)), errPacingDeadlineRejected) {
						t.Fatal("setter did not reject")
					}
				}
				go peer.Read(make([]byte, 1))
				if n, err := conn.Write([]byte{7}); n != 1 || err != nil {
					t.Fatalf("rejected setter changed pacing n%d err%v", n, err)
				}
				return
			}
			if mode == "EOF" {
				conn.SetReadDeadline(time.Now().Add(-time.Second))
				b := make([]byte, 1)
				n, err := conn.Read(b)
				if n != 1 || b[0] != 7 || !errors.Is(err, io.EOF) {
					t.Fatalf("EOF data n%d b%v err%v", n, b, err)
				}
				return
			}
			bucket.SetLimit(1)
			done := make(chan error, 1)
			go func() { _, err := conn.Write([]byte{7}); done <- err }()
			until := time.Now().Add(time.Second)
			for bucket.Tokens() > -0.5 && time.Now().Before(until) {
				time.Sleep(time.Millisecond)
			}
			conn.Close()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("close err%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("close ignored")
			}
			if plain.writes.Load() != 0 {
				t.Fatal("closed pacing reached socket Write")
			}
		})
	}
}

type pacingAllocationSink struct{ net.Conn }

func (c *pacingAllocationSink) Write(p []byte) (int, error)      { return len(p), nil }
func (c *pacingAllocationSink) SetWriteDeadline(time.Time) error { return nil }
func TestPacingDeadlineHotPathDoesNotAllocate(t *testing.T) {
	socket, peer := net.Pipe()
	defer peer.Close()
	plain := &pacingAllocationSink{Conn: socket}
	bucket := rate.NewLimiter(rate.Inf, 65535)
	conn := (&Limiter{bucket: bucket}).Wrap(context.Background(), plain)
	defer conn.Close()
	payload := make([]byte, 1200)
	conn.Write(payload)
	if allocs := testing.AllocsPerRun(1000, func() {
		conn.SetWriteDeadline(time.Now().Add(time.Minute))
		conn.Write(payload)
	}); allocs != 0 {
		t.Fatalf("warmed immediate pacing allocated %g times", allocs)
	}
}
func BenchmarkPacingDeadlineHotPath(b *testing.B) {
	socket, peer := net.Pipe()
	defer peer.Close()
	plain := &pacingAllocationSink{Conn: socket}
	bucket := rate.NewLimiter(rate.Inf, 65535)
	conn := (&Limiter{bucket: bucket}).Wrap(context.Background(), plain)
	defer conn.Close()
	payload := make([]byte, 1200)
	conn.Write(payload)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn.Write(payload)
	}
}
