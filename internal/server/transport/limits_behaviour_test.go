package transport

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// The connection limiter, tested by what it does rather than by what its
// callers look like.
//
// limitrelease_test.go and pairingtimeout_test.go both read the source of the
// transports and assert that a `release()` appears on a particular branch.
// That is a reasonable way to check six near-identical copies at once, and it
// is not a test of the limiter: it would pass unchanged if acquire() stopped
// counting, if release() went negative, or if two goroutines could both take
// the last slot.
//
// This package is the least covered in the tree — 12.6% across 9,103 lines —
// and the limiter is the part of it that has already leaked in production. So
// it gets tested directly.

func TestAnUnlimitedTunnelPaysNothing(t *testing.T) {
	// The zero value has to enforce nothing, because most tunnels configure no
	// limits at all and the unlimited path is the hot one.
	if l := newLimiter(Limits{}); l != nil {
		t.Errorf("newLimiter(no limits) = %v, want nil so callers pay only a nil check", l)
	}

	// And a nil limiter has to survive every method, because that is exactly
	// what the transports call it with.
	var nilLim *limiter
	if !nilLim.acquire() {
		t.Error("a nil limiter refused a connection; unlimited must mean unlimited")
	}
	nilLim.release() // must not panic
	nilLim.waitBytes(context.Background(), 1<<20)
	conn := &fakeConn{}
	if got := nilLim.wrap(context.Background(), conn); got != net.Conn(conn) {
		t.Error("a nil limiter wrapped a connection; there is nothing to pace")
	}
}

func TestTheConnectionCapIsExactAndSlotsComeBack(t *testing.T) {
	const cap = 3
	l := newLimiter(Limits{MaxConnections: cap})
	if l == nil {
		t.Fatal("newLimiter returned nil for a tunnel that has a connection cap")
	}

	for i := 0; i < cap; i++ {
		if !l.acquire() {
			t.Fatalf("slot %d of %d was refused", i+1, cap)
		}
	}
	// One past the cap.
	if l.acquire() {
		t.Fatalf("a %dth connection was admitted on a cap of %d", cap+1, cap)
	}
	// A refused acquire must not consume anything: this is the leak shape —
	// the count went up and was never brought back down, so the tunnel
	// eventually refused everything with a limit that looked right in the
	// config.
	l.release()
	if !l.acquire() {
		t.Error("after one release a slot was still refused; a refused acquire is " +
			"holding a slot it never got")
	}

	// Releasing everything returns to the starting state rather than drifting
	// negative, which would let the tunnel exceed its own cap later.
	for i := 0; i < cap; i++ {
		l.release()
	}
	for i := 0; i < cap; i++ {
		if !l.acquire() {
			t.Fatalf("after a full release cycle, slot %d was refused", i+1)
		}
	}
	if l.acquire() {
		t.Error("the cap drifted: more than the configured number of slots are available")
	}
}

// The cap has to hold when the connections arrive at once, which is the only
// way they actually arrive. Run under -race this also covers the counter
// itself.
func TestTheConnectionCapHoldsUnderConcurrentAccepts(t *testing.T) {
	const (
		cap     = 8
		callers = 200
	)
	l := newLimiter(Limits{MaxConnections: cap})

	var (
		mu       sync.Mutex
		admitted int
		peak     int
		wg       sync.WaitGroup
	)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !l.acquire() {
				return
			}
			mu.Lock()
			admitted++
			if admitted > peak {
				peak = admitted
			}
			mu.Unlock()

			time.Sleep(time.Millisecond) // hold the slot briefly

			mu.Lock()
			admitted--
			mu.Unlock()
			l.release()
		}()
	}
	wg.Wait()

	if peak > cap {
		t.Errorf("%d connections were live at once on a cap of %d", peak, cap)
	}
	if admitted != 0 {
		t.Errorf("%d slots are still marked in use after every caller finished", admitted)
	}
	// And the limiter agrees it is empty.
	for i := 0; i < cap; i++ {
		if !l.acquire() {
			t.Fatalf("the limiter leaked: slot %d unavailable after everything released", i+1)
		}
	}
}

// A bandwidth cap wraps; a connection cap alone does not. Wrapping when there
// is nothing to pace would put a layer on the hot path for no reason, and
// failing to wrap when there is would silently ignore the cap.
func TestOnlyABandwidthCapWrapsTheConnection(t *testing.T) {
	plain := &fakeConn{}

	connsOnly := newLimiter(Limits{MaxConnections: 4})
	if got := connsOnly.wrap(context.Background(), plain); got != net.Conn(plain) {
		t.Error("a connection-count cap wrapped the connection; there is no pacing to do")
	}

	withBandwidth := newLimiter(Limits{BandwidthMbps: 10})
	wrapped := withBandwidth.wrap(context.Background(), plain)
	if wrapped == net.Conn(plain) {
		t.Fatal("a bandwidth cap did not wrap the connection, so the cap does nothing")
	}
	if _, ok := wrapped.(*limitedConn); !ok {
		t.Errorf("wrap returned %T, want *limitedConn", wrapped)
	}
}

// The bandwidth cap has to actually delay. A bucket that is consulted and never
// waited on is a setting that reads as applied and is not — the shape this
// codebase has already been bitten by twice.
func TestTheBandwidthCapActuallyPaces(t *testing.T) {
	// 1 Mbit/s = 125,000 bytes/s, and the burst is one second's worth. Charging
	// three bursts must take at least two seconds of waiting, whatever the
	// machine.
	l := newLimiter(Limits{BandwidthMbps: 1})
	if l == nil || l.bucket == nil {
		t.Fatal("a bandwidth cap produced no bucket")
	}
	const perSecond = 125_000

	l.waitBytes(context.Background(), perSecond) // the burst, which is free
	start := time.Now()
	l.waitBytes(context.Background(), 2*perSecond)
	elapsed := time.Since(start)

	if elapsed < 1500*time.Millisecond {
		t.Errorf("charging two seconds of traffic took %s; the cap is not pacing anything", elapsed)
	}
}

// A request larger than the bucket can never be satisfied in one go, so it is
// charged in bucket-sized pieces. Without that it would fail outright and the
// bytes would go through unpaced — which is worse than slow.
func TestAWriteLargerThanTheBucketIsChargedInPieces(t *testing.T) {
	l := newLimiter(Limits{BandwidthMbps: 100}) // 12.5 MB/s, burst the same
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.waitBytes(context.Background(), 40<<20) // 40 MB, several times the burst
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("charging a write larger than the bucket never returned; it is either " +
			"refused outright or waiting for tokens that can never arrive at once")
	}
}

// fakeConn is a net.Conn that does nothing, for the wrap tests.
type fakeConn struct{ net.Conn }

// Tearing a tunnel down must not wait on a token bucket.
//
// The pacing used to be charged against context.Background(), with a note
// saying the connection's own deadline covered it. It does not: WaitN blocks
// *before* the Read or Write it is pacing, so a deadline on the socket never
// reaches it and closing the connection does not either. A generation being
// torn down would sit here paying out tokens for a connection already on its
// way out.
func TestPacingStopsWhenTheGenerationEnds(t *testing.T) {
	// 1 Mbit/s, and then several seconds' worth charged at once — long enough
	// that returning quickly can only mean the context was honoured.
	l := newLimiter(Limits{BandwidthMbps: 1})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		l.waitBytes(ctx, 10*125_000) // ten seconds of traffic
		done <- time.Since(start)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case took := <-done:
		if took > 2*time.Second {
			t.Errorf("pacing took %s to notice the generation had ended", took)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pacing never returned after the context was cancelled; teardown waits " +
			"on the token bucket")
	}
}
