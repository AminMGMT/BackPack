package control

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitFor(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting")
}

func TestAJobRunsAndKeepsItsResult(t *testing.T) {
	j := NewJobs()
	started, err := j.Start(context.Background(), "linktest", "tun-1",
		func(ctx context.Context, p *Progress) (any, error) { return 42, nil })
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started.State != Running {
		t.Fatalf("a freshly started job is %s", started.State)
	}

	waitFor(t, time.Second, func() bool {
		got, _ := j.Get(started.ID)
		return got.State.Done()
	})
	got, ok := j.Get(started.ID)
	if !ok {
		t.Fatal("the job disappeared")
	}
	if got.State != Succeeded {
		t.Fatalf("state = %s, want succeeded", got.State)
	}
	if got.Result != 42 {
		t.Fatalf("result = %v", got.Result)
	}
	if got.Target != "tun-1" {
		t.Fatalf("target = %q", got.Target)
	}
	if got.Ended.IsZero() {
		t.Fatal("a finished job has no end time")
	}
}

func TestAFailedJobKeepsItsReason(t *testing.T) {
	j := NewJobs()
	started, _ := j.Start(context.Background(), "speedtest", "",
		func(ctx context.Context, p *Progress) (any, error) {
			return nil, errors.New("the far end refused")
		})
	waitFor(t, time.Second, func() bool { g, _ := j.Get(started.ID); return g.State.Done() })

	got, _ := j.Get(started.ID)
	if got.State != Failed {
		t.Fatalf("state = %s", got.State)
	}
	if !strings.Contains(got.Err, "refused") {
		t.Fatalf("err = %q", got.Err)
	}
}

// Two path measurements at once measure each other. Refusing is the point, and
// handing back the running job rather than an apology is what a caller that
// raced actually wants.
func TestASecondJobOfTheSameKindIsRefusedWithTheFirst(t *testing.T) {
	j := NewJobs()
	release := make(chan struct{})
	first, _ := j.Start(context.Background(), "linktest", "a",
		func(ctx context.Context, p *Progress) (any, error) { <-release; return nil, nil })

	second, err := j.Start(context.Background(), "linktest", "b",
		func(ctx context.Context, p *Progress) (any, error) { return nil, nil })
	var busy ErrBusy
	if !errors.As(err, &busy) {
		t.Fatalf("a second job of the same kind was accepted: %v", err)
	}
	if second.ID != first.ID || busy.Running.ID != first.ID {
		t.Fatalf("the refusal did not carry the running job: %+v", second)
	}
	if busy.Running.Target != "a" {
		t.Fatalf("the refusal named the wrong target: %q", busy.Running.Target)
	}
	close(release)
}

// Different kinds are independent: a fleet rollout must not be blocked by
// somebody looking at a speed test.
func TestDifferentKindsRunTogether(t *testing.T) {
	j := NewJobs()
	release := make(chan struct{})
	work := func(ctx context.Context, p *Progress) (any, error) { <-release; return nil, nil }

	if _, err := j.Start(context.Background(), "linktest", "", work); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := j.Start(context.Background(), "rollout", "", work); err != nil {
		t.Fatalf("a different kind was refused: %v", err)
	}
	close(release)
}

// A kind becomes free again the moment its job finishes, or the second link
// test anybody runs is refused for ever.
func TestAKindIsFreeAgainAfterItsJobEnds(t *testing.T) {
	j := NewJobs()
	first, _ := j.Start(context.Background(), "linktest", "",
		func(ctx context.Context, p *Progress) (any, error) { return nil, nil })
	waitFor(t, time.Second, func() bool { g, _ := j.Get(first.ID); return g.State.Done() })

	if _, err := j.Start(context.Background(), "linktest", "",
		func(ctx context.Context, p *Progress) (any, error) { return nil, nil }); err != nil {
		t.Fatalf("the kind was still held after its job ended: %v", err)
	}
}

func TestProgressIsVisibleWhileTheJobRuns(t *testing.T) {
	j := NewJobs()
	reported := make(chan struct{})
	release := make(chan struct{})
	started, _ := j.Start(context.Background(), "rollout", "",
		func(ctx context.Context, p *Progress) (any, error) {
			p.Step("upgrading %s", "kharej-1")
			close(reported)
			<-release
			return nil, nil
		})
	<-reported
	waitFor(t, time.Second, func() bool {
		g, _ := j.Get(started.ID)
		return g.Step == "upgrading kharej-1"
	})
	close(release)
}

func TestCancellingStopsTheJob(t *testing.T) {
	j := NewJobs()
	started, _ := j.Start(context.Background(), "rollout", "",
		func(ctx context.Context, p *Progress) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
	if !j.Cancel(started.ID) {
		t.Fatal("Cancel reported nothing to cancel")
	}
	waitFor(t, time.Second, func() bool { g, _ := j.Get(started.ID); return g.State.Done() })

	got, _ := j.Get(started.ID)
	if got.State != Cancelled {
		t.Fatalf("state = %s, want cancelled", got.State)
	}
	// "context canceled" tells an operator nothing they did not already know,
	// and putting it in the error field makes a cancellation read as a fault.
	if got.Err != "" {
		t.Fatalf("a cancelled job reported an error: %q", got.Err)
	}
}

// A caller racing a job that has already finished is the ordinary case, not a
// mistake.
func TestCancellingAFinishedJobIsNotAnError(t *testing.T) {
	j := NewJobs()
	started, _ := j.Start(context.Background(), "linktest", "",
		func(ctx context.Context, p *Progress) (any, error) { return nil, nil })
	waitFor(t, time.Second, func() bool { g, _ := j.Get(started.ID); return g.State.Done() })
	if j.Cancel(started.ID) {
		t.Fatal("Cancel claimed to have stopped a job that had already finished")
	}
	if j.Cancel("no-such-job") {
		t.Fatal("Cancel claimed to have stopped a job that never existed")
	}
}

// A panel that has shut down must not leave a fleet rollout running against
// servers nobody is watching.
func TestTheParentContextStopsAJob(t *testing.T) {
	j := NewJobs()
	parent, cancel := context.WithCancel(context.Background())
	started, _ := j.Start(parent, "rollout", "",
		func(ctx context.Context, p *Progress) (any, error) { <-ctx.Done(); return nil, ctx.Err() })
	cancel()
	waitFor(t, time.Second, func() bool { g, _ := j.Get(started.ID); return g.State.Done() })
	if g, _ := j.Get(started.ID); g.State != Cancelled {
		t.Fatalf("state = %s, want cancelled", g.State)
	}
}

// What a panel polling after a refresh actually wants is "what is happening,
// or what just happened" — the old runners could only answer the first.
func TestLatestAnswersWhatIsHappeningOrWhatJustHappened(t *testing.T) {
	j := NewJobs()
	if _, ok := j.Latest("linktest"); ok {
		t.Fatal("Latest invented a job")
	}

	first, _ := j.Start(context.Background(), "linktest", "a",
		func(ctx context.Context, p *Progress) (any, error) { return "done", nil })
	waitFor(t, time.Second, func() bool { g, _ := j.Get(first.ID); return g.State.Done() })

	got, ok := j.Latest("linktest")
	if !ok || got.ID != first.ID || got.Result != "done" {
		t.Fatalf("Latest after finishing = %+v", got)
	}

	release := make(chan struct{})
	second, _ := j.Start(context.Background(), "linktest", "b",
		func(ctx context.Context, p *Progress) (any, error) { <-release; return nil, nil })
	got, _ = j.Latest("linktest")
	if got.ID != second.ID {
		t.Fatal("Latest preferred a finished job over a running one")
	}
	close(release)
}

func TestHistoryIsNewestFirstAndFiltersByKind(t *testing.T) {
	j := NewJobs()
	for _, kind := range []string{"linktest", "speedtest", "linktest"} {
		started, _ := j.Start(context.Background(), kind, "",
			func(ctx context.Context, p *Progress) (any, error) { return nil, nil })
		waitFor(t, time.Second, func() bool { g, _ := j.Get(started.ID); return g.State.Done() })
	}
	all := j.History("", 0)
	if len(all) != 3 {
		t.Fatalf("history holds %d", len(all))
	}
	links := j.History("linktest", 0)
	if len(links) != 2 {
		t.Fatalf("filtered history holds %d", len(links))
	}
	if links[0].Started.Before(links[1].Started) {
		t.Fatal("history is not newest first")
	}
	if one := j.History("", 1); len(one) != 1 {
		t.Fatalf("limit ignored: %d", len(one))
	}
}

// A long-lived panel must not grow a list nobody reads.
func TestHistoryIsBounded(t *testing.T) {
	j := NewJobs()
	j.keep = 5
	for i := 0; i < 20; i++ {
		started, _ := j.Start(context.Background(), "linktest", "",
			func(ctx context.Context, p *Progress) (any, error) { return nil, nil })
		waitFor(t, time.Second, func() bool { g, _ := j.Get(started.ID); return g.State.Done() })
	}
	if n := len(j.History("", 0)); n != 5 {
		t.Fatalf("history holds %d, cap is 5", n)
	}
}

// Get and History hand back copies, so a caller reads a job without holding a
// lock and without it changing underneath them.
func TestReadingIsSafeWhileJobsRun(t *testing.T) {
	j := NewJobs()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = j.Latest("linktest")
					_ = j.History("", 10)
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		started, err := j.Start(context.Background(), "linktest", "",
			func(ctx context.Context, p *Progress) (any, error) {
				p.Step("working")
				return i, nil
			})
		if err == nil {
			waitFor(t, time.Second, func() bool { g, _ := j.Get(started.ID); return g.State.Done() })
		}
	}
	close(stop)
	wg.Wait()
}

// A nil Progress must be safe: work written against this interface should not
// have to check.
func TestANilProgressIsSafe(t *testing.T) {
	var p *Progress
	p.Step("this must not panic")
	(&Progress{}).Step("nor this")
}

// Reading a result back as the type its kind produces.
//
// Result is `any` and stays that way: one registry holds every kind at once,
// and a registry generic over one T can hold only one of them. What this adds
// is that getting the type wrong says so rather than producing a zero value
// nobody notices.

func TestAResultComesBackAsItsOwnType(t *testing.T) {
	j := NewJobs()
	type measurement struct{ Ms int }

	started, _ := j.Start(context.Background(), "linktest", "",
		func(ctx context.Context, p *Progress) (any, error) {
			return &measurement{Ms: 42}, nil
		})
	waitFor(t, time.Second, func() bool { g, _ := j.Get(started.ID); return g.State.Done() })

	got, _ := j.Get(started.ID)
	m, ok := ResultOf[*measurement](got)
	if !ok {
		t.Fatal("a result could not be read back as the type that produced it")
	}
	if m.Ms != 42 {
		t.Fatalf("result = %+v", m)
	}
}

// The wrong type is a false rather than a panic, and the zero value rather than
// something half-built.
func TestReadingAResultAsTheWrongTypeIsRefused(t *testing.T) {
	j := NewJobs()
	started, _ := j.Start(context.Background(), "linktest", "",
		func(ctx context.Context, p *Progress) (any, error) { return "a string", nil })
	waitFor(t, time.Second, func() bool { g, _ := j.Get(started.ID); return g.State.Done() })

	got, _ := j.Get(started.ID)
	if n, ok := ResultOf[int](got); ok || n != 0 {
		t.Fatalf("a string came back as int %d", n)
	}
}

// A job that failed has no result to read, however well the type matches. A
// caller that skipped this would report the zero value as an answer.
func TestAFailedJobHasNoResult(t *testing.T) {
	j := NewJobs()
	started, _ := j.Start(context.Background(), "linktest", "",
		func(ctx context.Context, p *Progress) (any, error) {
			return "partial", errors.New("the far end went away")
		})
	waitFor(t, time.Second, func() bool { g, _ := j.Get(started.ID); return g.State.Done() })

	got, _ := j.Get(started.ID)
	if s, ok := ResultOf[string](got); ok {
		t.Fatalf("a failed job handed back %q as its result", s)
	}
}

// And one that is still running.
func TestARunningJobHasNoResultYet(t *testing.T) {
	j := NewJobs()
	release := make(chan struct{})
	started, _ := j.Start(context.Background(), "linktest", "",
		func(ctx context.Context, p *Progress) (any, error) { <-release; return "done", nil })

	if _, ok := ResultOf[string](started); ok {
		t.Fatal("a running job handed back a result")
	}
	close(release)
}
