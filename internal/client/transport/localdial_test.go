package transport

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// This message is the one users actually act on, so it is worth asserting on
// its content rather than just that something was logged.

func TestRefusedMessageExplainsTheRealCause(t *testing.T) {
	msg := localDialMessage("127.0.0.1:4545", syscall.ECONNREFUSED)

	for _, want := range []string{
		"127.0.0.1:4545",       // the address that failed
		"THIS server",          // which machine the problem is on
		"tunnel delivered",     // that the tunnel is not the problem
		"not running",          // first real cause
		"bound",                // second real cause
		"ss -tlnp | grep 4545", // what to run next
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q:\n%s", want, msg)
		}
	}
}

// A wrapped error must still be recognised — the dialer returns *net.OpError
// wrapping the syscall, never the bare errno.
func TestRefusedIsDetectedThroughWrapping(t *testing.T) {
	wrapped := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: &net.OpError{Op: "connect", Err: syscall.ECONNREFUSED},
	}
	if msg := localDialMessage("127.0.0.1:4545", wrapped); !strings.Contains(msg, "nothing is listening") {
		t.Errorf("a wrapped ECONNREFUSED was not recognised:\n%s", msg)
	}
}

// A real refused dial, so the test is tied to what the operating system
// actually returns rather than to a hand-built error.
func TestRealRefusedDialProducesTheRightMessage(t *testing.T) {
	// A port nothing is on. Bind and close to be sure it is free.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	_, dialErr := net.DialTimeout("tcp", addr, 2*time.Second)
	if dialErr == nil {
		t.Skip("something took the port between closing and dialling")
	}
	if msg := localDialMessage(addr, dialErr); !strings.Contains(msg, "nothing is listening") {
		t.Errorf("a real refused dial was not recognised:\n%s", msg)
	}
}

func TestTimeoutGetsItsOwnExplanation(t *testing.T) {
	timeoutErr := &net.OpError{Op: "dial", Err: &timeoutError{}}
	msg := localDialMessage("127.0.0.1:4545", timeoutErr)

	if !strings.Contains(msg, "timed out") {
		t.Errorf("a timeout should be described as one:\n%s", msg)
	}
	if strings.Contains(msg, "nothing is listening") {
		t.Errorf("a timeout is not the same as a refusal:\n%s", msg)
	}
}

func TestUnknownErrorStillSaysWhichMachine(t *testing.T) {
	msg := localDialMessage("127.0.0.1:4545", errors.New("something odd"))
	if !strings.Contains(msg, "THIS server") {
		t.Errorf("even an unrecognised error must place the fault:\n%s", msg)
	}
	if !strings.Contains(msg, "something odd") {
		t.Errorf("the original error must survive:\n%s", msg)
	}
}

// The whole point of rate limiting: a client retrying once a second must not
// bury the rest of the log.
func TestRepeatsAreSuppressed(t *testing.T) {
	logger, count := countingLogger()
	r := &localDialReporter{last: map[string]time.Time{}}

	for i := 0; i < 25; i++ {
		r.Report(logger, "127.0.0.1:4545", syscall.ECONNREFUSED)
	}
	if got := count(); got != 1 {
		t.Errorf("25 failures produced %d log lines, want 1", got)
	}
}

// Different destinations are different problems and must each be reported.
func TestDifferentAddressesAreReportedSeparately(t *testing.T) {
	logger, count := countingLogger()
	r := &localDialReporter{last: map[string]time.Time{}}

	r.Report(logger, "127.0.0.1:4545", syscall.ECONNREFUSED)
	r.Report(logger, "127.0.0.1:8080", syscall.ECONNREFUSED)
	r.Report(logger, "127.0.0.1:4545", syscall.ECONNREFUSED) // suppressed

	if got := count(); got != 2 {
		t.Errorf("got %d log lines, want 2 (one per address)", got)
	}
}

// After the quiet period the problem is reported again, so a log that is still
// being written shows the fault is ongoing.
func TestReportsAgainAfterTheQuietPeriod(t *testing.T) {
	logger, count := countingLogger()
	r := &localDialReporter{last: map[string]time.Time{}}

	r.Report(logger, "127.0.0.1:4545", syscall.ECONNREFUSED)
	// Backdate past the window rather than sleeping for it.
	r.mu.Lock()
	r.last["127.0.0.1:4545"] = time.Now().Add(-localDialQuiet - time.Second)
	r.mu.Unlock()
	r.Report(logger, "127.0.0.1:4545", syscall.ECONNREFUSED)

	if got := count(); got != 2 {
		t.Errorf("got %d log lines, want 2", got)
	}
}

// Reports arrive from one goroutine per forwarded connection.
func TestReporterIsConcurrencySafe(t *testing.T) {
	logger, _ := countingLogger()
	r := &localDialReporter{last: map[string]time.Time{}}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Report(logger, "127.0.0.1:4545", syscall.ECONNREFUSED)
		}(i)
	}
	wg.Wait()
}

func TestPortOnly(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:4545": "4545",
		"[::1]:8080":     "8080",
		"4545":           "4545",
	}
	for in, want := range cases {
		if got := portOnly(in); got != want {
			t.Errorf("portOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- helpers ----------------------------------------------------------------

// countingLogger returns a logger that discards output and a func reporting how
// many entries it wrote.
func countingLogger() (*logrus.Logger, func() int) {
	l := logrus.New()
	l.SetOutput(io.Discard)
	h := &countingHook{}
	l.AddHook(h)
	return l, h.count
}

type countingHook struct {
	mu sync.Mutex
	n  int
}

func (h *countingHook) Levels() []logrus.Level { return logrus.AllLevels }
func (h *countingHook) Fire(*logrus.Entry) error {
	h.mu.Lock()
	h.n++
	h.mu.Unlock()
	return nil
}
func (h *countingHook) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.n
}

type timeoutError struct{}

func (timeoutError) Error() string { return "i/o timeout" }
func (timeoutError) Timeout() bool { return true }
