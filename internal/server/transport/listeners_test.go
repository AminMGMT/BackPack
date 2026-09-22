package transport

import (
	"context"
	"sync"
	"testing"
	"time"
)

// The counter Start waits on.
//
// A WaitGroup was the first attempt and the race detector rejected it on sight:
// its contract is that every Add happens before Wait, and these Adds happen
// inside listener goroutines the transport launched and did not wait for. A
// listener binding a moment after shutdown begins is an Add racing a Wait.

func TestWaitReturnsOnceEveryListenerHasClosed(t *testing.T) {
	var l listenerSet
	l.hold()
	l.hold()

	done := make(chan struct{})
	go func() { defer close(done); l.wait(context.Background()) }()

	select {
	case <-done:
		t.Fatal("wait returned while two listeners were still held")
	case <-time.After(50 * time.Millisecond):
	}

	l.release()
	select {
	case <-done:
		t.Fatal("wait returned with one listener still held")
	case <-time.After(50 * time.Millisecond):
	}

	l.release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("wait did not return after the last listener closed")
	}
}

// Nothing held is nothing to wait for, and it must not block — a transport that
// never started still has its Start return.
func TestWaitingOnNothingReturnsAtOnce(t *testing.T) {
	var l listenerSet
	done := make(chan struct{})
	go func() { defer close(done); l.wait(context.Background()) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("wait blocked with nothing held")
	}
}

// A listener that binds *after* the wait has started is the case a WaitGroup
// could not express. It must reopen the gate rather than being missed.
func TestAListenerThatArrivesLateStillCounts(t *testing.T) {
	var l listenerSet
	l.hold()

	done := make(chan struct{})
	go func() { defer close(done); l.wait(context.Background()) }()

	time.Sleep(20 * time.Millisecond)
	l.hold()    // a second listener, after wait is already blocked
	l.release() // the first closes

	select {
	case <-done:
		t.Fatal("wait returned while a late listener was still held")
	case <-time.After(50 * time.Millisecond):
	}

	l.release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("wait never returned")
	}
}

// A listener that will not close is a bug, and holding the process open for it
// turns that bug into a hang. systemd escalates to SIGKILL either way, and the
// operator learns nothing from a service that refuses to stop.
func TestWaitGivesUpRatherThanHangingForever(t *testing.T) {
	var l listenerSet
	l.hold() // and never released

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	start := time.Now()
	l.wait(ctx)
	if took := time.Since(start); took > time.Second {
		t.Fatalf("wait held on for %v past a cancelled context", took)
	}
}

// hold and release are called from every listener goroutine a transport has,
// which is as many as it has forwarded ports.
func TestTheCounterIsSafeUnderConcurrentListeners(t *testing.T) {
	var l listenerSet
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.hold()
			time.Sleep(time.Millisecond)
			l.release()
		}()
	}
	// A waiter running alongside them, as Start is.
	waiting := make(chan struct{})
	go func() { defer close(waiting); l.wait(context.Background()) }()

	wg.Wait()
	select {
	case <-waiting:
	case <-time.After(2 * time.Second):
		t.Fatal("wait never returned after every listener closed")
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n != 0 {
		t.Fatalf("the counter settled at %d rather than zero", l.n)
	}
}

// Releasing more than was held must not drive the counter negative and leave
// wait unable to ever return.
func TestAnExtraReleaseDoesNotBreakTheCounter(t *testing.T) {
	var l listenerSet
	l.hold()
	l.release()
	l.release() // one too many

	l.mu.Lock()
	n := l.n
	l.mu.Unlock()
	if n != 0 {
		t.Fatalf("the counter is %d after an extra release", n)
	}

	l.hold()
	done := make(chan struct{})
	go func() { defer close(done); l.wait(context.Background()) }()
	l.release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the counter never recovered")
	}
}
