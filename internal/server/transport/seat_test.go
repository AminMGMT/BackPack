package transport

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A loop that fails just as its replacement is seated must not empty the seat
// of the client that replaced it. Run many times over, since it is a race.
func TestALateLostCannotUnseatTheClientThatReplacedIt(t *testing.T) {
	for i := 0; i < 500; i++ {
		var seat clientSeat
		var mu sync.Mutex
		holder := ""
		vacate := func() { mu.Lock(); holder = ""; mu.Unlock() }
		install := func(who string) func() { return func() { mu.Lock(); holder = who; mu.Unlock() } }

		lostA := make(chan func(), 1)
		seat.sit(context.Background(), vacate, install("a"), func(_ context.Context, lost func()) { lostA <- lost })
		lost := <-lostA

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); lost() }()
		go func() {
			defer wg.Done()
			seat.sit(context.Background(), vacate, install("b"), func(context.Context, func()) {})
		}()
		wg.Wait()

		mu.Lock()
		got := holder
		mu.Unlock()
		if got != "b" {
			t.Fatalf("run %d: the seat holds %q after b was seated; a's late failure emptied it", i, got)
		}
	}
}

// Seating a new client ends the previous client's loop and vacates it once.
func TestSeatingAClientEndsThePreviousOne(t *testing.T) {
	var seat clientSeat
	var vacated atomic.Int32
	ended := make(chan struct{})
	seat.sit(context.Background(), func() { vacated.Add(1) }, func() {},
		func(ctx context.Context, _ func()) { <-ctx.Done(); close(ended) })
	if seat.serving() != true {
		t.Fatal("a seated generation does not report itself serving")
	}
	seat.sit(context.Background(), func() { vacated.Add(1) }, func() {}, func(context.Context, func()) {})
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("the replaced client's loop was not ended")
	}
	if vacated.Load() != 1 {
		t.Fatalf("vacated %d times, want once", vacated.Load())
	}
}

// One client re-dialing, however often, is not a rivalry; two hosts taking
// turns is, and is said once rather than on every turn.
func TestRivalClientsAreNamedButARedialIsNot(t *testing.T) {
	var r rivalry
	now := time.Now()
	for i := 0; i < 20; i++ {
		if _, rival := r.seat("198.51.100.4:4000", now.Add(time.Duration(i)*time.Second)); rival {
			t.Fatal("one client re-dialing was taken for two")
		}
	}

	var r2 rivalry
	warnings := 0
	var named []string
	for i := 0; i < 20; i++ {
		addr := "198.51.100.4:4000"
		if i%2 == 1 {
			addr = "203.0.113.9:5000"
		}
		if hosts, rival := r2.seat(addr, now.Add(time.Duration(i)*time.Second)); rival {
			warnings++
			named = hosts
		}
	}
	if warnings != 1 {
		t.Fatalf("two clients taking turns were reported %d times in 20 seconds, want once", warnings)
	}
	if len(named) != 2 || named[0] != "198.51.100.4" || named[1] != "203.0.113.9" {
		t.Fatalf("named %v", named)
	}
}
