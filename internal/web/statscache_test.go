package web

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The collection sleeps a whole second and then walks the process table. The
// point of the cache is that ten pollers arriving together cost one of those,
// not ten.
func TestConcurrentCallersShareOneCollection(t *testing.T) {
	var c statsCache
	var collections atomic.Int32

	gather := func() (*SystemStats, error) {
		collections.Add(1)
		time.Sleep(50 * time.Millisecond) // stands in for the real second
		return &SystemStats{CPUUsage: "12.00"}, nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stats, err := c.collect(gather)
			if err != nil {
				t.Errorf("collect: %v", err)
				return
			}
			if stats.CPUUsage != "12.00" {
				t.Errorf("got %q", stats.CPUUsage)
			}
		}()
	}
	wg.Wait()

	if got := collections.Load(); got != 1 {
		t.Fatalf("ten simultaneous callers caused %d collections, want 1", got)
	}
}

// A second call inside the TTL is served from what the first one got.
func TestASecondCallInsideTheTTLIsFree(t *testing.T) {
	var c statsCache
	var collections atomic.Int32
	gather := func() (*SystemStats, error) {
		collections.Add(1)
		return &SystemStats{CPUUsage: "5.00"}, nil
	}

	for i := 0; i < 5; i++ {
		if _, err := c.collect(gather); err != nil {
			t.Fatal(err)
		}
	}
	if got := collections.Load(); got != 1 {
		t.Fatalf("%d collections inside one TTL, want 1", got)
	}
}

// Once the snapshot is stale, the next caller pays for a fresh one.
func TestAStaleSnapshotIsCollectedAgain(t *testing.T) {
	var c statsCache
	var collections atomic.Int32
	gather := func() (*SystemStats, error) {
		collections.Add(1)
		return &SystemStats{}, nil
	}

	if _, err := c.collect(gather); err != nil {
		t.Fatal(err)
	}
	// Age the snapshot rather than sleeping out the TTL.
	c.mu.Lock()
	c.taken = time.Now().Add(-statsCacheTTL - time.Second)
	c.mu.Unlock()

	if _, err := c.collect(gather); err != nil {
		t.Fatal(err)
	}
	if got := collections.Load(); got != 2 {
		t.Fatalf("%d collections, want a second one after the snapshot went stale", got)
	}
}

// A failure must not be held for the length of the TTL — a host that could not
// be read a moment ago is worth asking again.
func TestAFailureIsNotCached(t *testing.T) {
	var c statsCache
	var collections atomic.Int32
	failing := errors.New("no stats today")

	gather := func() (*SystemStats, error) {
		if collections.Add(1) == 1 {
			return nil, failing
		}
		return &SystemStats{CPUUsage: "1.00"}, nil
	}

	if _, err := c.collect(gather); !errors.Is(err, failing) {
		t.Fatalf("first call err = %v, want the failure", err)
	}
	stats, err := c.collect(gather)
	if err != nil {
		t.Fatalf("second call err = %v, want it to have retried", err)
	}
	if stats.CPUUsage != "1.00" {
		t.Fatalf("got %q from the retry", stats.CPUUsage)
	}
}

// Everyone waiting on one collection gets its error, not a nil result.
func TestWaitersSeeTheFailureTheySharedIn(t *testing.T) {
	var c statsCache
	failing := errors.New("collection failed")
	release := make(chan struct{})

	gather := func() (*SystemStats, error) {
		<-release
		return nil, failing
	}

	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = c.collect(gather)
		}(i)
	}
	time.Sleep(50 * time.Millisecond) // let them all pile onto the one call
	close(release)
	wg.Wait()

	for i, err := range errs {
		if !errors.Is(err, failing) {
			t.Errorf("waiter %d got err = %v, want the shared failure", i, err)
		}
	}
}
