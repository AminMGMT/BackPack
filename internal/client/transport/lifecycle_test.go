package transport

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// The generation-scoped state every client transport rebuilds on reconnect.
//
// clientState exists because Restart() swaps the context, the control channel,
// the usage monitor and the counters while the previous generation's goroutines
// are still winding down — and those goroutines read the very fields being
// replaced. The values are pointers and channels, so an unsynchronised read
// there is not merely stale, it is undefined.
//
// That reasoning is written down in the source and nothing checked it. This
// package is the least covered in the tree (9.5% across 6,069 lines) and this
// type is the part of it every one of the seven transports depends on, so it is
// where coverage is worth the most.

func TestAGenerationIsPublishedWhole(t *testing.T) {
	var s clientState

	// Nothing before the first Reset: a transport that reads before it has
	// started must get nil rather than a half-built generation.
	if s.Ctx() != nil || s.Cancel() != nil || s.Usage() != nil {
		t.Fatal("clientState handed out state before any generation existed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.Reset(ctx, cancel, nil)

	if s.Ctx() != ctx {
		t.Error("Ctx did not return the generation's context")
	}
	if s.Cancel() == nil {
		t.Error("Cancel was not published with the generation")
	}
	// Reset clears the control channels: they belong to the generation being
	// replaced, and carrying one across would have the new generation writing
	// to the old one's socket.
	if s.Conn() != nil || s.WSConn() != nil {
		t.Error("Reset kept a control channel from the previous generation")
	}
	cancel()
}

// The second Reset is the one that matters: it happens while the first
// generation is still shutting down.
func TestAReconnectReplacesTheWholeGenerationAtOnce(t *testing.T) {
	var s clientState

	ctx1, cancel1 := context.WithCancel(context.Background())
	s.Reset(ctx1, cancel1, nil)
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	s.SetConn(c1)

	ctx2, cancel2 := context.WithCancel(context.Background())
	s.Reset(ctx2, cancel2, nil)
	defer cancel2()

	if s.Ctx() == ctx1 {
		t.Error("the old context survived a reconnect")
	}
	if s.Conn() != nil {
		t.Error("the old control channel survived a reconnect; the new generation " +
			"would write to the socket the old one is closing")
	}
	cancel1()
}

// Readers and the reconnect run at the same time by construction. Under -race
// this is the test that says the lock is doing its job.
func TestReadingAGenerationWhileItIsBeingReplacedIsSafe(t *testing.T) {
	var s clientState
	ctx, cancel := context.WithCancel(context.Background())
	s.Reset(ctx, cancel, nil)
	defer cancel()

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Readers, as the transports' goroutines are.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = s.Ctx()
				_ = s.Cancel()
				_ = s.Conn()
				_ = s.WSConn()
				_ = s.Usage()
			}
		}()
	}

	// And Restart, swapping underneath them.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			c, cn := context.WithCancel(context.Background())
			s.Reset(c, cn, nil)
			cn()
		}
	}()

	time.Sleep(120 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// CloseConn has to survive a generation that never got a control channel —
// which is every generation that failed to dial, and there are many of those.
func TestClosingAGenerationWithNoControlChannelIsHarmless(t *testing.T) {
	var s clientState
	ctx, cancel := context.WithCancel(context.Background())
	s.Reset(ctx, cancel, nil)
	defer cancel()
	s.CloseConn() // must not panic
}

// drain empties the signal channel rather than replacing it. Replacing races
// with the goroutines selecting on the old one — which is what it used to do.
func TestDrainEmptiesTheChannelWithoutReplacingIt(t *testing.T) {
	ch := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		ch <- struct{}{}
	}
	before := ch

	drain(ch)

	if len(ch) != 0 {
		t.Errorf("drain left %d signals in the channel", len(ch))
	}
	if ch != before {
		t.Error("drain replaced the channel; goroutines selecting on the old one would " +
			"wait for a signal that can never come")
	}
	// And on an already-empty channel it returns rather than blocking.
	done := make(chan struct{})
	go func() { drain(ch); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("drain blocked on an empty channel")
	}
}

// The status line is written by three writers across two overlapping
// generations. It was a plain string field until that was noticed.
func TestTheStatusLineIsSafeUnderConcurrentWriters(t *testing.T) {
	var ts tunnelStatus
	ts.set("connecting")
	if ts.get() != "connecting" {
		t.Fatalf("get returned %q", ts.get())
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if i%2 == 0 {
					ts.set("connected")
				} else {
					_ = ts.get()
				}
			}
		}(i)
	}
	wg.Wait()
}
