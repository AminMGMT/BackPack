package web

import (
	"sync"
	"time"
)

// Collecting the host's statistics is expensive, and it was done once per
// request.
//
// One collection samples the network counters, sleeps a whole second so a rate
// can be worked out, and then walks the process table to count connections.
// The dashboard polls this endpoint on a timer, so a page left open on two
// screens meant two of those a few seconds apart, each holding a goroutine
// asleep for a second and each doing the walk again — and anything that
// scrapes the endpoint faster, or a browser that reloads in a loop, multiplied
// it from there. Nothing about the answer changes fast enough to be worth
// that.
//
// So a collection is shared: whoever arrives while one is running waits for
// it and gets its result, and a result stays good for statsCacheTTL. The
// figures that are cheap and want to be live — the tunnel's status, its
// traffic total — are refreshed on the way out, so what the cache holds is
// only the part that was slow to get.

// statsCacheTTL is how long a collected snapshot is served for. The collection
// itself already takes a second, so this is the gap between the end of one and
// the start of the next.
const statsCacheTTL = 2 * time.Second

type statsCache struct {
	mu sync.Mutex
	// done is non-nil while a collection is in flight, and is closed when it
	// finishes. Waiters take a copy of it and wait on that rather than holding
	// the lock across the collection.
	done  chan struct{}
	value *SystemStats
	err   error
	taken time.Time
}

// collect returns a recent set of statistics, calling gather at most once at a
// time and at most once per statsCacheTTL.
func (c *statsCache) collect(gather func() (*SystemStats, error)) (*SystemStats, error) {
	c.mu.Lock()

	// Fresh enough to serve as it is. An error is not cached: a failure that
	// has passed should be retried rather than repeated for two seconds.
	if c.value != nil && c.err == nil && time.Since(c.taken) < statsCacheTTL {
		value := c.value
		c.mu.Unlock()
		return value, nil
	}

	// Somebody is already doing the work; wait for theirs instead of starting
	// a second one.
	if c.done != nil {
		wait := c.done
		c.mu.Unlock()
		<-wait

		c.mu.Lock()
		value, err := c.value, c.err
		c.mu.Unlock()
		return value, err
	}

	done := make(chan struct{})
	c.done = done
	c.mu.Unlock()

	value, err := gather()

	c.mu.Lock()
	c.value, c.err, c.taken = value, err, time.Now()
	c.done = nil
	c.mu.Unlock()
	// Released after the result is stored, so no waiter can wake to find it
	// missing.
	close(done)

	return value, err
}
