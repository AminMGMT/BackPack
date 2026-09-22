package chain

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fast returns a chain wound down to test speed. Everything about the state
// machine is the same; only the durations differ.
func fast(primary string, fallbacks ...string) *Chain {
	c := New(primary, fallbacks, 200*time.Millisecond)
	c.poll = 5 * time.Millisecond
	c.teardown = 5 * time.Millisecond
	c.regrace = 100 * time.Millisecond
	c.windowFloor = time.Millisecond
	return c
}

// recorder captures the order candidates were started in.
type recorder struct {
	mu    sync.Mutex
	order []string
	// settle names the candidates that report themselves up.
	settle map[string]bool
}

func (r *recorder) start(ctx context.Context, name string) Attempt {
	r.mu.Lock()
	r.order = append(r.order, name)
	up := r.settle[name]
	r.mu.Unlock()
	return Attempt{Settled: func() bool { return up }}
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

func TestNewDropsBlanksAndDuplicates(t *testing.T) {
	c := New("tcp", []string{"", "tcp", "quic", "quic", "ws"}, time.Second)
	got := c.Candidates()
	want := []string{"tcp", "quic", "ws"}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", got, want)
		}
	}
}

// Single decides whether the rotation machinery runs at all, so getting it
// wrong in the permissive direction silently disables fallback on every tunnel
// configured for it — the chain would be built, reported, and never used.
// Mutation testing found this: the suite covered Single indirectly and would
// not have noticed the comparison inverted.
func TestSingleOnlyWhenThereIsNothingToFallBackTo(t *testing.T) {
	for _, tc := range []struct {
		name       string
		primary    string
		fallbacks  []string
		wantSingle bool
	}{
		{"nothing configured", "", nil, true},
		{"one candidate", "tcp", nil, true},
		{"a duplicate is not a fallback", "tcp", []string{"tcp", ""}, true},
		{"two candidates", "tcp", []string{"quic"}, false},
		{"three candidates", "tcp", []string{"quic", "ws"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New(tc.primary, tc.fallbacks, time.Second)
			if got := c.Single(); got != tc.wantSingle {
				t.Fatalf("Single() = %v with candidates %v, want %v",
					got, c.Candidates(), tc.wantSingle)
			}
		})
	}
}

// A chain with nothing to fall back to must behave exactly like the code it
// replaces: start the one transport and wait. This is the default
// configuration, so it is the case that must not regress.
func TestSingleCandidateNeverRotates(t *testing.T) {
	c := fast("tcp")
	if !c.Single() {
		t.Fatal("Single() = false for a one-candidate chain")
	}
	r := &recorder{settle: map[string]bool{}}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	c.Run(ctx, true, r.start)

	// It never settles, and the window expires several times over — but with
	// nowhere to go it must be started exactly once and left alone.
	if got := r.seen(); len(got) != 1 || got[0] != "tcp" {
		t.Fatalf("a lone candidate was restarted: %v", got)
	}
}

func TestRotatesPastCandidatesThatNeverComeUp(t *testing.T) {
	c := fast("quic", "ws", "tcp")
	r := &recorder{settle: map[string]bool{"tcp": true}}

	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { defer close(done); c.Run(ctx, true, r.start) }()

	// tcp is third and the only one that settles, so the chain must reach it
	// and then stop moving.
	deadline := time.After(3 * time.Second)
	for {
		if got := r.seen(); len(got) == 3 && got[2] == "tcp" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("never reached tcp: %v", r.seen())
		case <-time.After(5 * time.Millisecond):
		}
	}
	order := r.seen()
	if order[0] != "quic" || order[1] != "ws" {
		t.Fatalf("tried out of order: %v", order)
	}

	// Settled means settled: give it well over a dwell and nothing else starts.
	time.Sleep(500 * time.Millisecond)
	if got := r.seen(); len(got) != 3 {
		t.Fatalf("kept rotating after a candidate came up: %v", got)
	}
	cancel()
	<-done
}

func TestRemembersTheCandidateThatWorked(t *testing.T) {
	c := fast("quic", "tcp")
	var mu sync.Mutex
	var remembered []string
	c.OnSettled(func(name string) {
		mu.Lock()
		remembered = append(remembered, name)
		mu.Unlock()
	})
	r := &recorder{settle: map[string]bool{"tcp": true}}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.Run(ctx, true, r.start)

	mu.Lock()
	defer mu.Unlock()
	if len(remembered) != 1 || remembered[0] != "tcp" {
		t.Fatalf("remembered %v, want [tcp]", remembered)
	}
}

// A settled candidate that drops briefly must be left alone: every transport
// reconnects on its own, and rotating through a blip would turn a short outage
// into a long one.
func TestATransientDropDoesNotRotate(t *testing.T) {
	c := fast("tcp", "quic")
	var mu sync.Mutex
	up := true
	started := 0

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	go func() {
		// Down for well under the grace period, then back.
		time.Sleep(300 * time.Millisecond)
		mu.Lock()
		up = false
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		up = true
		mu.Unlock()
	}()

	c.Run(ctx, true, func(ctx context.Context, name string) Attempt {
		mu.Lock()
		started++
		mu.Unlock()
		return Attempt{Settled: func() bool {
			mu.Lock()
			defer mu.Unlock()
			return name == "tcp" && up
		}}
	})

	mu.Lock()
	defer mu.Unlock()
	if started != 1 {
		t.Fatalf("a %v blip rotated the chain: %d starts", 50*time.Millisecond, started)
	}
}

// A settled candidate that stays down past the grace period is the case the
// chain exists for: the carrier really has been taken away.
func TestASustainedDropRotatesOnward(t *testing.T) {
	c := fast("tcp", "quic")
	var mu sync.Mutex
	up := true
	var order []string

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		time.Sleep(200 * time.Millisecond)
		mu.Lock()
		up = false
		mu.Unlock()
	}()

	c.Run(ctx, true, func(ctx context.Context, name string) Attempt {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
		return Attempt{Settled: func() bool {
			mu.Lock()
			defer mu.Unlock()
			return name == "tcp" && up
		}}
	})

	mu.Lock()
	defer mu.Unlock()
	if len(order) < 3 {
		t.Fatalf("a sustained drop did not rotate: %v", order)
	}
	// It gives up on tcp, tries quic, and comes back round to tcp a sweep
	// later — the list wraps, so a carrier that was blocked is retried without
	// needing a rule of its own.
	if order[0] != "tcp" || order[1] != "quic" || order[2] != "tcp" {
		t.Fatalf("did not rotate onward: %v", order)
	}
}

// The rendezvous argument the package comment makes is a property of
// attemptWindow, and it is the only reason the two ends meet without talking.
func TestClientSweepsTheWholeListInsideOneServerDwell(t *testing.T) {
	c := New("a", []string{"b", "c", "d"}, 60*time.Second)

	server := c.attemptWindow(false)
	client := c.attemptWindow(true)

	if server != 60*time.Second {
		t.Fatalf("server window = %v, want the whole dwell", server)
	}
	if sweep := client * time.Duration(len(c.Candidates())); sweep > server {
		t.Fatalf("a client sweep takes %v but the server only holds for %v: "+
			"the two ends can miss each other for ever", sweep, server)
	}
}

// The rendezvous test above asserts a bound, which is the right shape for the
// property but leaves the arithmetic untested: mutation testing showed that
// "two candidates" could be treated as "one" — no division, so a client holds a
// blocked carrier for the server's whole dwell — without a single test
// noticing. These are the exact figures.
func TestTheAttemptWindowIsTheDwellDividedByTheCandidates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		primary    string
		fallbacks  []string
		dwell      time.Duration
		sweep      bool
		wantWindow time.Duration
	}{
		{"a server holds a candidate for the whole dwell", "a", []string{"b", "c"}, 60 * time.Second, false, 60 * time.Second},
		{"one candidate is never divided", "a", nil, 60 * time.Second, true, 60 * time.Second},
		{"two candidates halve it", "a", []string{"b"}, 60 * time.Second, true, 30 * time.Second},
		{"three candidates take a third each", "a", []string{"b", "c"}, 60 * time.Second, true, 20 * time.Second},
		{"the floor wins when the share is smaller", "a", []string{"b", "c", "d", "e", "f"}, 6 * time.Second, true, 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New(tc.primary, tc.fallbacks, tc.dwell)
			if got := c.attemptWindow(tc.sweep); got != tc.wantWindow {
				t.Fatalf("attemptWindow(%v) = %v with %d candidates and a %v dwell, want %v",
					tc.sweep, got, len(c.Candidates()), tc.dwell, tc.wantWindow)
			}
		})
	}
}

// Dividing the dwell must not produce a window so short that a candidate is
// rejected for being slow rather than for being blocked.
func TestTheClientWindowHasAFloor(t *testing.T) {
	c := New("a", []string{"b", "c", "d", "e", "f"}, 6*time.Second)
	if got := c.attemptWindow(true); got < 5*time.Second {
		t.Fatalf("client window = %v, below the dial-timeout floor", got)
	}
}

// Each candidate must be torn down before the next is started, or the next one
// finds the forwarded ports still held.
func TestTheOutgoingCandidateIsCancelledBeforeTheNextStarts(t *testing.T) {
	c := fast("a", "b")
	var mu sync.Mutex
	var live int
	var maxLive int

	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()

	c.Run(ctx, true, func(ctx context.Context, name string) Attempt {
		mu.Lock()
		live++
		if live > maxLive {
			maxLive = live
		}
		mu.Unlock()
		go func() {
			<-ctx.Done()
			mu.Lock()
			live--
			mu.Unlock()
		}()
		return Attempt{Settled: func() bool { return false }}
	})

	mu.Lock()
	defer mu.Unlock()
	if maxLive > 1 {
		t.Fatalf("%d candidates were live at once; they would fight for the forwarded ports", maxLive)
	}
}

func TestRunReturnsWhenTheContextIsDone(t *testing.T) {
	c := fast("tcp", "quic")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx, true, func(ctx context.Context, name string) Attempt {
			return Attempt{Settled: func() bool { return false }}
		})
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

// A caller that does not distinguish states hands back a nil Settled, and the
// chain must then leave that candidate alone rather than treating "unknown" as
// "down" and rotating for ever.
func TestANilSettledOwnsTheTunnel(t *testing.T) {
	c := fast("tcp", "quic")
	var mu sync.Mutex
	starts := 0
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	c.Run(ctx, true, func(ctx context.Context, name string) Attempt {
		mu.Lock()
		starts++
		mu.Unlock()
		return Attempt{}
	})
	mu.Lock()
	defer mu.Unlock()
	if starts != 1 {
		t.Fatalf("starts = %d, want 1", starts)
	}
}
