package transport

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

type pacingProbeConn struct {
	net.Conn
	writes    atomic.Int32
	entered   chan struct{}
	readReady chan struct{}
	readEOF   bool
}

func (c *pacingProbeConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	return len(p), nil
}

func (c *pacingProbeConn) Read(p []byte) (int, error) {
	close(c.entered)
	<-c.readReady
	copy(p, "tail")
	if c.readEOF {
		return 4, io.EOF
	}
	return 4, nil
}

func TestClosingAPacedConnectionInterruptsItsWait(t *testing.T) {
	l := newLimiter(Limits{BandwidthMbps: 1})
	socket, peer := net.Pipe()
	defer peer.Close()
	plain := &pacingProbeConn{Conn: socket}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := l.wrap(ctx, plain)
	defer c.Close()
	l.waitBytes(ctx, 125000)
	done := make(chan error, 1)
	go func() { _, err := c.Write(make([]byte, 125000)); done <- err }()
	time.Sleep(20 * time.Millisecond)
	c.Close()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("closed pacing write returned %v", err)
		}
	case <-time.After(150 * time.Millisecond):
		cancel()
		<-done
		t.Fatal("closed connection retained its pacing worker")
	}
	if ctx.Err() != nil || plain.writes.Load() != 0 {
		t.Fatalf("parent=%v, underlying writes=%d", ctx.Err(), plain.writes.Load())
	}
}

func TestPacingCancellationNeverStartsAWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	socket, peer := net.Pipe()
	defer socket.Close()
	defer peer.Close()
	plain := &pacingProbeConn{Conn: socket}
	c := newLimiter(Limits{BandwidthMbps: 1}).wrap(ctx, plain)
	defer c.Close()
	n, err := c.Write([]byte("rejected"))
	if n != 0 || !errors.Is(err, context.Canceled) || plain.writes.Load() != 0 {
		t.Fatalf("cancelled write n=%d err=%v underlying writes=%d", n, err, plain.writes.Load())
	}
}

func TestPacingReadKeepsBytesAndReportsCancellation(t *testing.T) {
	for _, eof := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "tailEOF"}[eof], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			socket, peer := net.Pipe()
			defer socket.Close()
			defer peer.Close()
			plain := &pacingProbeConn{Conn: socket, entered: make(chan struct{}), readReady: make(chan struct{}), readEOF: eof}
			l := newLimiter(Limits{BandwidthMbps: 1})
			l.waitBytes(ctx, 125000)
			c := l.wrap(ctx, plain)
			defer c.Close()
			done := make(chan error, 1)
			go func() {
				buf := make([]byte, 8)
				n, err := c.Read(buf)
				if n != 4 || string(buf[:n]) != "tail" {
					t.Errorf("read n=%d payload=%q", n, buf[:n])
				}
				done <- err
			}()
			<-plain.entered
			cancel()
			close(plain.readReady)
			select {
			case err := <-done:
				want := error(context.Canceled)
				if eof {
					want = io.EOF
				}
				if !errors.Is(err, want) {
					t.Errorf("read error=%v, want %v", err, want)
				}
			case <-time.After(150 * time.Millisecond):
				t.Fatal("read pacing ignored cancellation")
			}
		})
	}
}

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
			conn := (&limiter{bucket: bucket}).wrap(context.Background(), plain)
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
			conn := (&limiter{bucket: bucket}).wrap(context.Background(), plain)
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
	conn := (&limiter{bucket: bucket}).wrap(context.Background(), plain)
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
	conn := (&limiter{bucket: bucket}).wrap(context.Background(), plain)
	defer conn.Close()
	payload := make([]byte, 1200)
	conn.Write(payload)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn.Write(payload)
	}
}
