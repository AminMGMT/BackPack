package transport

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// The shared pairing loop, run rather than read.
//
// The guards next door scan the transports' source for the right shape, which
// is what caught the original leak in four copies at once. This tests the shape
// itself: every path out of a pairing either releases the connection slot or
// hands it to the relay, and there is no third option.

func quietLog() *logrus.Logger {
	l := logrus.New()
	l.SetLevel(logrus.FatalLevel)
	return l
}

// pairedConn is a local connection that records whether it was closed.
type pairedConn struct {
	net.Conn
	mu     sync.Mutex
	closed bool
}

func (f *pairedConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *pairedConn) wasClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// LocalAddr is reached by localForwardPort in some paths; a nil address would
// panic there, so this returns something real.
func (f *pairedConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
}

// takenLimiter is a limiter with one slot already taken, which is the state a
// pairing always starts in: the slot was reserved on accept.
func takenLimiter(t *testing.T) *limiter {
	t.Helper()
	l := newLimiter(Limits{MaxConnections: 4})
	if !l.acquire() {
		t.Fatal("setup: could not take a slot")
	}
	return l
}

func newPairing(t *testing.T, ctx context.Context, tunnel <-chan net.Conn,
	l *limiter, local *pairedConn, age time.Duration) pairing[net.Conn] {
	t.Helper()
	return pairing[net.Conn]{
		ctx:      ctx,
		local:    LocalTCPConn{conn: local, remoteAddr: "127.0.0.1:80", timeCreated: nowMillis() - age.Milliseconds()},
		tunnel:   tunnel,
		limits:   l,
		log:      quietLog(),
		announce: func(net.Conn, string) error { return nil },
		discard:  func(c net.Conn) { c.Close() },
		relay:    func(net.Conn, LocalTCPConn) {},
	}
}

// The case the whole thing exists for: nothing ever arrives.
//
// The old shape checked the age once and then blocked, so on a pool that had
// run dry the timeout could not fire — which is the one case it is for.
func TestAPairingThatIsNeverAnsweredTimesOutAndGivesBackItsSlot(t *testing.T) {
	l := takenLimiter(t)
	local := &pairedConn{}
	// Aged so the remaining wait is short; the timer still has to be the thing
	// that ends it.
	p := newPairing(t, context.Background(), make(chan net.Conn), l, local,
		pairingTimeout-150*time.Millisecond)

	done := make(chan struct{})
	go func() { defer close(done); p.run() }()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a pairing with nothing coming never timed out")
	}
	if !local.wasClosed() {
		t.Error("the timed-out connection was left open")
	}
	if l.active.Load() != 0 {
		t.Errorf("%d slot(s) still held after a timeout — a tunnel with max_connections "+
			"loses one to every timeout until it refuses everything", l.active.Load())
	}
}

// A run ending while a connection is parked here used to leak one socket and
// one slot, on every restart.
func TestAPairingInterruptedByShutdownGivesBackItsSlot(t *testing.T) {
	l := takenLimiter(t)
	local := &pairedConn{}
	ctx, cancel := context.WithCancel(context.Background())
	p := newPairing(t, ctx, make(chan net.Conn), l, local, 0)

	done := make(chan struct{})
	go func() { defer close(done); p.run() }()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a pairing did not return when the run ended")
	}
	if !local.wasClosed() {
		t.Error("the parked connection was left open when the run ended")
	}
	if l.active.Load() != 0 {
		t.Errorf("%d slot(s) still held after shutdown", l.active.Load())
	}
}

// The successful path hands the slot to the relay rather than releasing it —
// the transfer has not finished yet, and releasing here would let the limit be
// exceeded by everything currently carrying traffic.
func TestASuccessfulPairingHandsTheSlotToTheRelay(t *testing.T) {
	l := takenLimiter(t)
	local := &pairedConn{}
	tunnel := make(chan net.Conn, 1)
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	tunnel <- client

	var relayed bool
	p := newPairing(t, context.Background(), tunnel, l, local, 0)
	p.relay = func(c net.Conn, lc LocalTCPConn) { relayed = true }
	p.run()

	if !relayed {
		t.Fatal("a pairing that got a tunnel connection did not relay it")
	}
	if local.wasClosed() {
		t.Error("the local connection was closed on the successful path")
	}
	if l.active.Load() != 1 {
		t.Errorf("the slot was released before the transfer started (in use: %d)", l.active.Load())
	}
}

// A tunnel connection that cannot be announced on is unusable, and nothing else
// will close it. The local connection is still good and must keep waiting.
func TestAnUnusableTunnelConnectionIsDiscardedAndTheLocalOneKeepsWaiting(t *testing.T) {
	l := takenLimiter(t)
	local := &pairedConn{}
	tunnel := make(chan net.Conn, 2)

	bad := &pairedConn{}
	good := &pairedConn{}
	tunnel <- bad
	tunnel <- good

	var announced int
	var asked int
	p := newPairing(t, context.Background(), tunnel, l, local, 0)
	p.announce = func(c net.Conn, _ string) error {
		announced++
		if c == net.Conn(bad) {
			return errors.New("the far end went away")
		}
		return nil
	}
	p.discard = func(c net.Conn) { c.Close() }
	p.request = func() { asked++ }

	var relayedWith net.Conn
	p.relay = func(c net.Conn, _ LocalTCPConn) { relayedWith = c }
	p.run()

	if announced != 2 {
		t.Fatalf("announced %d times, want a retry after the bad one", announced)
	}
	if !bad.wasClosed() {
		t.Error("the unusable tunnel connection was left open")
	}
	if relayedWith != net.Conn(good) {
		t.Error("the pairing did not go on to the next tunnel connection")
	}
	if local.wasClosed() {
		t.Error("the local connection was closed over a bad tunnel connection")
	}
	if asked != 1 {
		t.Errorf("asked for another tunnel connection %d times, want 1 — a pool that "+
			"has run dry never refills otherwise", asked)
	}
	if l.active.Load() != 1 {
		t.Errorf("the slot was released on the retry path (in use: %d)", l.active.Load())
	}
}

// The transports that top the pool up elsewhere leave request nil, and a nil
// hook must not be a panic on the retry path.
func TestANilRequestHookIsSafe(t *testing.T) {
	l := takenLimiter(t)
	local := &pairedConn{}
	tunnel := make(chan net.Conn, 1)
	bad := &pairedConn{}
	tunnel <- bad

	p := newPairing(t, context.Background(), tunnel, l, local,
		pairingTimeout-150*time.Millisecond)
	p.announce = func(net.Conn, string) error { return errors.New("no") }
	p.request = nil

	done := make(chan struct{})
	go func() { defer close(done); p.run() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a pairing with a nil request hook hung")
	}
}

// expired and drop are what a transport does before a pairing is started.
// Giving a connection that queued for the whole timeout a fresh timer would
// double its life.
func TestAStaleConnectionIsDroppedRatherThanGivenAFreshTimer(t *testing.T) {
	fresh := LocalTCPConn{timeCreated: nowMillis()}
	if expired(fresh) {
		t.Error("a fresh connection was called expired")
	}
	old := LocalTCPConn{timeCreated: nowMillis() - pairingTimeout.Milliseconds() - 1}
	if !expired(old) {
		t.Error("a connection past the timeout was not called expired")
	}

	l := takenLimiter(t)
	conn := &pairedConn{}
	drop(LocalTCPConn{conn: conn, timeCreated: nowMillis() - pairingTimeout.Milliseconds()},
		l, quietLog())
	if !conn.wasClosed() {
		t.Error("drop left the connection open")
	}
	if l.active.Load() != 0 {
		t.Errorf("drop left %d slot(s) held", l.active.Load())
	}
}

// The property the whole file is for, stated once: under a mix of timeouts,
// shutdowns and successes, the limiter always comes back to where it started.
func TestNoPathOutOfAPairingLeavesASlotHeld(t *testing.T) {
	l := newLimiter(Limits{MaxConnections: 64})
	log := quietLog()

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		if !l.acquire() {
			t.Fatal("setup: the limiter ran out")
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			local := &pairedConn{}
			tunnel := make(chan net.Conn, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			p := pairing[net.Conn]{
				ctx: ctx, tunnel: tunnel, limits: l, log: log,
				local:    LocalTCPConn{conn: local, timeCreated: nowMillis() - pairingTimeout.Milliseconds() + 80},
				announce: func(net.Conn, string) error { return nil },
				discard:  func(c net.Conn) { c.Close() },
				// The successful path hands the slot on; release it here so
				// every case ends at the same place.
				relay: func(net.Conn, LocalTCPConn) { l.release() },
			}
			switch i % 3 {
			case 0: // pair it
				tunnel <- &pairedConn{}
			case 1: // end the run under it
				go func() { time.Sleep(10 * time.Millisecond); cancel() }()
			case 2: // let it time out
			}
			p.run()
		}(i)
	}
	wg.Wait()

	if l.active.Load() != 0 {
		t.Fatalf("%d of 30 slots were never given back", l.active.Load())
	}
}
