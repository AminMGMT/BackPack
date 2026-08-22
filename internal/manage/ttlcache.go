package manage

import (
	"sync"
	"time"
)

// A shared answer for a question that is expensive to ask and cheap to reuse.
//
// The panel polls: system stats every four seconds, the tunnel list every six,
// and the log drawer every two while it is open. Behind those polls sit forks —
// journalctl for a unit's log, systemctl for whether a unit is running — and
// each one used to be performed per request, per tunnel. On a host with a
// handful of tunnels and a panel open on two screens that is a steady stream of
// processes, which is most of what the panel costs in CPU.
//
// Two properties matter, and the second is the one that bites without it:
//
//   - A result is reused for ttl, so polls faster than that cost nothing extra.
//   - A call arriving while an identical one is running waits for it instead of
//     starting a second. Without this, any read slower than the poll interval
//     overlaps with the next, the overlaps stack, and the load climbs on its
//     own until whatever is behind it falls over.
//
// It is the same shape as web.statsCache, which exists for the same reason on
// the same kind of endpoint.

type ttlEntry[T any] struct {
	// done is non-nil while a read is in flight and is closed when it
	// finishes. Waiters copy it and wait on that rather than holding the cache
	// lock across the work.
	done  chan struct{}
	value T
	taken time.Time
}

type ttlCache[T any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	prune   time.Duration
	entries map[string]*ttlEntry[T]
}

func newTTLCache[T any](ttl, prune time.Duration) *ttlCache[T] {
	return &ttlCache[T]{ttl: ttl, prune: prune, entries: make(map[string]*ttlEntry[T])}
}

// get returns a fresh cached value, waits for one already being read, or reads
// it. read is never called concurrently for the same key.
func (c *ttlCache[T]) get(key string, read func() T) T {
	for {
		c.mu.Lock()
		e := c.entries[key]

		switch {
		case e != nil && e.done != nil:
			// Somebody is already asking. Wait for their answer rather than
			// starting a second fork beside theirs — this is the case that
			// multiplies.
			wait := e.done
			c.mu.Unlock()
			<-wait
			continue

		case e != nil && time.Since(e.taken) < c.ttl:
			value := e.value
			c.mu.Unlock()
			return value
		}

		// Ours to run. The entry is published with done set before the lock is
		// released, so everyone arriving from here on waits above.
		e = &ttlEntry[T]{done: make(chan struct{})}
		if c.entries == nil {
			c.entries = make(map[string]*ttlEntry[T])
		}
		c.entries[key] = e
		c.pruneLocked()
		c.mu.Unlock()

		value := read()

		c.mu.Lock()
		finished := e.done
		e.value, e.taken, e.done = value, time.Now(), nil
		c.mu.Unlock()
		close(finished)

		return value
	}
}

// forget drops everything cached, for when something has just made it wrong.
//
// A read already in flight is left alone: its waiters are owed an answer, and
// it will simply be a moment out of date, which is what they would have got
// anyway.
func (c *ttlCache[T]) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if e.done == nil {
			delete(c.entries, k)
		}
	}
}

// pruneLocked drops entries nothing has asked for in a long time, so a host
// whose tunnels are renamed or deleted does not accumulate them. Called with
// the lock held, on the path that is already about to do something expensive.
func (c *ttlCache[T]) pruneLocked() {
	for k, e := range c.entries {
		if e.done == nil && time.Since(e.taken) > c.prune {
			delete(c.entries, k)
		}
	}
}
