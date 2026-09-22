package network

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Dialling several addresses at once and keeping the first that answers.
//
// The endpoint list is walked one at a time: dial the current address, and on
// failure rotate to the next. A filtered IP does not refuse a connection, it
// swallows it — so the cost of a dead first address is a full dial timeout,
// ten seconds by default, on every single reconnect. With three addresses and
// the live one last, a tunnel spends twenty seconds down before it starts
// trying the thing that works.
//
// The property that makes this safe rather than merely fast is the one about
// the losers: exactly one connection survives a race and every other one is
// closed. A racer that leaks the runners-up leaks a socket per reconnect, on
// the path that runs most when the network is worst.

// fakeConn is something to win or lose a race with.
type fakeConn struct {
	addr   string
	closed atomic.Bool
}

func (c *fakeConn) Close() error { c.closed.Store(true); return nil }

func TestARaceReturnsTheFirstAddressThatAnswers(t *testing.T) {
	dial := func(ctx context.Context, addr string) (*fakeConn, error) {
		if addr == "slow" {
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &fakeConn{addr: addr}, nil
	}

	conn, addr, err := Race(context.Background(),
		[]string{"slow", "fast"}, 20*time.Millisecond, dial)
	if err != nil {
		t.Fatalf("Race: %v", err)
	}
	if addr != "fast" || conn.addr != "fast" {
		t.Fatalf("won by %q, want the one that answered first", addr)
	}
}

// The first address still gets a head start. Racing them all from the same
// instant would make every reconnect open N connections to N servers and keep
// one — which is rude to the servers and, on a mobile link, expensive.
func TestTheFirstAddressIsGivenAHeadStart(t *testing.T) {
	var started sync.Map
	dial := func(ctx context.Context, addr string) (*fakeConn, error) {
		started.Store(addr, time.Now())
		if addr == "first" {
			select {
			case <-time.After(200 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &fakeConn{addr: addr}, nil
	}

	_, _, err := Race(context.Background(),
		[]string{"first", "second"}, 80*time.Millisecond, dial)
	if err != nil {
		t.Fatalf("Race: %v", err)
	}

	a, _ := started.Load("first")
	b, ok := started.Load("second")
	if !ok {
		t.Fatal("the second address was never tried")
	}
	if gap := b.(time.Time).Sub(a.(time.Time)); gap < 60*time.Millisecond {
		t.Fatalf("the second dial started %v after the first; it should wait out "+
			"the stagger so a working first address is not raced for nothing", gap)
	}
}

// An address that answers immediately must not be raced at all.
func TestAWorkingFirstAddressIsNotRaced(t *testing.T) {
	var dials atomic.Int32
	dial := func(ctx context.Context, addr string) (*fakeConn, error) {
		dials.Add(1)
		return &fakeConn{addr: addr}, nil
	}

	_, addr, err := Race(context.Background(),
		[]string{"a", "b", "c"}, 100*time.Millisecond, dial)
	if err != nil {
		t.Fatalf("Race: %v", err)
	}
	if addr != "a" {
		t.Fatalf("won by %q, want the first", addr)
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("%d dials for an address that answered at once", n)
	}
}

// The property that makes this safe: one survivor, everything else closed.
func TestEveryLoserIsClosed(t *testing.T) {
	var mu sync.Mutex
	var made []*fakeConn

	dial := func(ctx context.Context, addr string) (*fakeConn, error) {
		// Everything answers, at slightly different times, so several are in
		// flight at once and all but one must be cleaned up.
		if addr != "c" {
			select {
			case <-time.After(150 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		c := &fakeConn{addr: addr}
		mu.Lock()
		made = append(made, c)
		mu.Unlock()
		return c, nil
	}

	winner, addr, err := Race(context.Background(),
		[]string{"a", "b", "c"}, 10*time.Millisecond, dial)
	if err != nil {
		t.Fatalf("Race: %v", err)
	}
	if addr != "c" {
		t.Fatalf("won by %q", addr)
	}

	// Give the losers time to finish and be cleaned up.
	time.Sleep(400 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	for _, c := range made {
		if c == winner {
			if c.closed.Load() {
				t.Fatal("the winning connection was closed")
			}
			continue
		}
		if !c.closed.Load() {
			t.Fatalf("the connection to %q was left open; that is a socket leaked "+
				"per reconnect, on the path that runs most when the network is worst",
				c.addr)
		}
	}
}

// When nothing answers, the caller needs to know what every address said —
// "connection refused" and "i/o timeout" send an operator to different places.
func TestWhenNothingAnswersEveryFailureIsReported(t *testing.T) {
	dial := func(ctx context.Context, addr string) (*fakeConn, error) {
		return nil, fmt.Errorf("%s: no route", addr)
	}

	_, _, err := Race(context.Background(),
		[]string{"a", "b"}, 10*time.Millisecond, dial)
	if err == nil {
		t.Fatal("a race where nothing answered reported success")
	}
	for _, want := range []string{"a", "b", "no route"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not mention %q: %v", want, err)
		}
	}
}

// One address is the ordinary case and must behave exactly as a plain dial.
func TestASingleAddressIsJustADial(t *testing.T) {
	var dials atomic.Int32
	dial := func(ctx context.Context, addr string) (*fakeConn, error) {
		dials.Add(1)
		return &fakeConn{addr: addr}, nil
	}
	_, addr, err := Race(context.Background(), []string{"only"}, time.Second, dial)
	if err != nil || addr != "only" {
		t.Fatalf("Race(%q) = %q, %v", "only", addr, err)
	}
	if dials.Load() != 1 {
		t.Fatalf("%d dials for one address", dials.Load())
	}
}

func TestAnEmptyListIsAnError(t *testing.T) {
	dial := func(ctx context.Context, addr string) (*fakeConn, error) {
		t.Fatal("dialled something from an empty list")
		return nil, nil
	}
	if _, _, err := Race(context.Background(), nil, time.Second, dial); err == nil {
		t.Fatal("racing nothing reported success")
	}
}

// Cancelling has to stop every dial in flight, or a tunnel that is shutting
// down leaves connections being opened behind it.
func TestCancellingStopsEveryDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var live atomic.Int32

	dial := func(ctx context.Context, addr string) (*fakeConn, error) {
		live.Add(1)
		defer live.Add(-1)
		<-ctx.Done()
		return nil, ctx.Err()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		Race(ctx, []string{"a", "b", "c"}, time.Millisecond, dial)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Race did not return after its context was cancelled")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if live.Load() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%d dials were still running after cancellation", live.Load())
}

// A dial that returns a nil connection and a nil error must not win: that is a
// dialler misbehaving, and treating it as a winner hands the caller nothing to
// use and closes the connection that actually worked.
func TestANilConnectionDoesNotWin(t *testing.T) {
	dial := func(ctx context.Context, addr string) (*fakeConn, error) {
		if addr == "liar" {
			return nil, nil
		}
		return &fakeConn{addr: addr}, nil
	}
	conn, addr, err := Race(context.Background(),
		[]string{"liar", "real"}, 10*time.Millisecond, dial)
	if err != nil {
		t.Fatalf("Race: %v", err)
	}
	if conn == nil || addr != "real" {
		t.Fatalf("a dialler returning (nil, nil) won the race: %q", addr)
	}
}

var _ = errors.New
