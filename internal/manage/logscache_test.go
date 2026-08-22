package manage

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The panel's log drawer polls every two seconds, and every poll used to fork
// journalctl. These are the properties that stop that from reaching journald.

// Repeated asks inside the window cost one read.
func TestRepeatedAsksShareOneJournalRead(t *testing.T) {
	c := newTTLCache[string](logsCacheTTL, logsCachePrune)
	var reads atomic.Int32
	read := func() string {
		reads.Add(1)
		return "lines"
	}

	for i := 0; i < 20; i++ {
		if got := c.get("tunnel", read); got != "lines" {
			t.Fatalf("call %d returned %q", i, got)
		}
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("20 asks inside the window ran journalctl %d times, want 1", n)
	}
}

// Callers arriving while a read is running wait for it instead of starting
// their own. This is the case that used to multiply: a read slower than the
// poll interval meant every tick added another journalctl.
func TestASlowReadIsNotStartedTwice(t *testing.T) {
	c := newTTLCache[string](logsCacheTTL, logsCachePrune)
	var reads atomic.Int32
	release := make(chan struct{})
	read := func() string {
		reads.Add(1)
		<-release
		return "lines"
	}

	const callers = 12
	var wg sync.WaitGroup
	got := make([]string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = c.get("tunnel", read)
		}(i)
	}

	// Let them all pile up on the one read, the way the drawer's ticks did.
	time.Sleep(150 * time.Millisecond)
	if n := reads.Load(); n != 1 {
		t.Errorf("%d concurrent askers started %d reads, want 1", callers, n)
	}
	close(release)
	wg.Wait()

	if n := reads.Load(); n != 1 {
		t.Errorf("after they finished, %d reads had run, want 1", n)
	}
	for i, g := range got {
		if g != "lines" {
			t.Errorf("caller %d got %q, not the shared result", i, g)
		}
	}
}

// The cache is a bound, not a freeze: once the window passes the next ask reads
// again, or the drawer would stop updating.
func TestTheJournalIsReadAgainOnceTheWindowPasses(t *testing.T) {
	c := newTTLCache[string](logsCacheTTL, logsCachePrune)
	var reads atomic.Int32
	read := func() string {
		return fmt.Sprintf("read-%d", reads.Add(1))
	}

	first := c.get("tunnel", read)
	// Age the entry past the window rather than sleeping for it.
	c.mu.Lock()
	c.entries["tunnel"].taken = time.Now().Add(-logsCacheTTL - time.Millisecond)
	c.mu.Unlock()

	second := c.get("tunnel", read)
	if first == second {
		t.Errorf("the drawer would never update: both asks returned %q", first)
	}
	if n := reads.Load(); n != 2 {
		t.Errorf("reads = %d, want 2", n)
	}
}

// Two tunnels are two different questions and must not answer each other.
func TestEachTunnelIsCachedSeparately(t *testing.T) {
	c := newTTLCache[string](logsCacheTTL, logsCachePrune)
	a := c.get("iran", func() string { return "iran-lines" })
	b := c.get("kharej", func() string { return "kharej-lines" })

	if a != "iran-lines" || b != "kharej-lines" {
		t.Errorf("got %q and %q; the two tunnels shared an entry", a, b)
	}
}

// A host whose tunnels come and go must not accumulate entries forever.
func TestStaleEntriesArePruned(t *testing.T) {
	c := newTTLCache[string](logsCacheTTL, logsCachePrune)
	c.get("gone", func() string { return "x" })

	c.mu.Lock()
	c.entries["gone"].taken = time.Now().Add(-logsCachePrune - time.Second)
	c.mu.Unlock()

	// Any read takes the prune path on its way through.
	c.get("current", func() string { return "y" })

	c.mu.Lock()
	_, stillThere := c.entries["gone"]
	c.mu.Unlock()
	if stillThere {
		t.Error("an entry nothing has asked for in minutes was kept")
	}
}

// The panel asks "is this unit running" per tunnel on two different timers, and
// each ask used to fork systemctl. These cover the sharing that stops it, and
// the invalidation that keeps the panel honest when a unit is acted on.

func TestUnitStateIsSharedBetweenPollers(t *testing.T) {
	c := newTTLCache[bool](unitStateTTL, unitStatePrune)
	var forks atomic.Int32
	ask := func() bool {
		forks.Add(1)
		return true
	}

	// GatherSystem and GatherTunnels, several tunnels, several panels.
	for i := 0; i < 30; i++ {
		if !c.get("is-active\x00backpack-iran", ask) {
			t.Fatalf("call %d disagreed with the cached answer", i)
		}
	}
	if n := forks.Load(); n != 1 {
		t.Errorf("30 asks about one unit forked systemctl %d times, want 1", n)
	}
}

// Different units are different questions.
func TestEachUnitIsAskedAboutSeparately(t *testing.T) {
	c := newTTLCache[bool](unitStateTTL, unitStatePrune)
	up := c.get("is-active\x00up", func() bool { return true })
	down := c.get("is-active\x00down", func() bool { return false })
	if !up || down {
		t.Errorf("up=%v down=%v; the two units shared an entry", up, down)
	}
}

// Acting on a unit has to be visible immediately. A Stop that still reads as
// running for two seconds would look like the button did nothing.
func TestActingOnAUnitDropsTheCachedState(t *testing.T) {
	c := newTTLCache[bool](unitStateTTL, unitStatePrune)
	running := true
	ask := func() bool { return running }

	if !c.get("is-active\x00svc", ask) {
		t.Fatal("the unit should start out reported as running")
	}
	running = false // as though StopService had just run
	if !c.get("is-active\x00svc", ask) {
		t.Fatal("the cache should still be serving the old answer at this point")
	}

	c.forget()
	if c.get("is-active\x00svc", ask) {
		t.Error("after forget() the next ask must re-read; the panel would show a stopped unit as running")
	}
}

// forget must not strand callers waiting on a read that is still running.
func TestForgetDoesNotStrandWaiters(t *testing.T) {
	c := newTTLCache[bool](unitStateTTL, unitStatePrune)
	release := make(chan struct{})
	started := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		c.get("slow", func() bool { close(started); <-release; return true })
	}()
	<-started
	go func() {
		defer wg.Done()
		c.get("slow", func() bool { return false }) // must wait, not start its own
	}()

	time.Sleep(100 * time.Millisecond)
	c.forget() // a Stop lands while the read is in flight
	close(release)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("forget() during an in-flight read left a caller waiting forever")
	}
}
