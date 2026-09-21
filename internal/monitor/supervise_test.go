package monitor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// quiet keeps the supervisor's own error lines out of the test output; what is
// being checked is what it does, not what it says.
func quiet() *logrus.Logger {
	l := logrus.New()
	l.SetLevel(logrus.PanicLevel)
	return l
}

// A job that panics has to come back.
//
// Containing the panic per job was already right — a bug in the Telegram bot
// must not take the watchdog down with it. What was missing is what happened
// next: the job stopped for the life of the process, and if that job was the
// watchdog then self-healing was silently off from then on, with one line in a
// log nobody reads at the time as the only trace.
func TestAPanickingJobIsRestarted(t *testing.T) {
	var runs atomic.Int32
	done := make(chan struct{})

	fn := func(ctx context.Context) {
		if runs.Add(1) == 1 {
			panic("first run explodes")
		}
		close(done) // the second run is the restart
		<-ctx.Done()
	}

	// The real backoff starts at five seconds, which is right in production and
	// far too slow here.
	restore := shortenRestartBackoff(t)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go superviseJob(ctx, quiet(), "test job", fn)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("the job panicked and was never restarted (ran %d times)", runs.Load())
	}
}

// A job that keeps panicking is eventually left down, rather than restarted
// for ever. Spinning on a job that cannot run is its own problem.
func TestAJobThatAlwaysPanicsIsEventuallyLeftDown(t *testing.T) {
	var runs atomic.Int32
	fn := func(ctx context.Context) {
		runs.Add(1)
		panic("always")
	}

	restore := shortenRestartBackoff(t)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	returned := make(chan struct{})
	go func() { superviseJob(ctx, quiet(), "doomed job", fn); close(returned) }()

	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("the supervisor never gave up; a job that cannot run would be restarted for ever")
	}
	if got := runs.Load(); int(got) != jobRestartLimit {
		t.Errorf("the job ran %d times, want exactly jobRestartLimit (%d)", got, jobRestartLimit)
	}
}

// A job that simply returns is not a failure and must not be restarted.
func TestAJobThatReturnsCleanlyIsNotRestarted(t *testing.T) {
	var runs atomic.Int32
	fn := func(ctx context.Context) { runs.Add(1) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	superviseJob(ctx, quiet(), "well-behaved job", fn)

	if got := runs.Load(); got != 1 {
		t.Errorf("a job that returned cleanly ran %d times, want 1", got)
	}
}

// Shutting down while a job is in its restart backoff must not hold the
// process open for the length of that backoff.
func TestShutdownDuringTheBackoffReturnsImmediately(t *testing.T) {
	fn := func(ctx context.Context) { panic("so it goes into backoff") }

	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() { superviseJob(ctx, quiet(), "job", fn); close(returned) }()

	// Let it panic and settle into the wait, then stop it.
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("the supervisor sat out its backoff after the context ended; a shutdown " +
			"would wait up to five minutes for it")
	}
}

// shortenRestartBackoff drives the supervisor at test speed and puts the real
// figures back afterwards.
func shortenRestartBackoff(t *testing.T) func() {
	t.Helper()
	first, max := jobRestartFirst, jobRestartMax
	jobRestartFirst, jobRestartMax = 5*time.Millisecond, 20*time.Millisecond
	return func() { jobRestartFirst, jobRestartMax = first, max }
}
