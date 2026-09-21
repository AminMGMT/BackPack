package metrics

import (
	"os"
	"runtime"
	"sync"
	"testing"
)

// The counters have to move when the thing they count moves, or they are
// decoration. Both leaks this codebase has had were a number climbing and not
// coming back down; a counter that does not track its subject would have shown
// a flat line through both.
func TestTheRuntimeCountersTrackWhatTheyCount(t *testing.T) {
	before := readRuntimeStats()
	if before.Goroutines < 1 {
		t.Fatalf("goroutine count came back as %d, which cannot be right", before.Goroutines)
	}

	// Hold a known number of goroutines and see the count move by at least
	// that much. "At least", not "exactly": the test binary and the runtime
	// have goroutines of their own that come and go.
	const held = 50
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(held)
	for i := 0; i < held; i++ {
		go func() { started.Done(); <-release }()
	}
	started.Wait()

	during := readRuntimeStats()
	if during.Goroutines < before.Goroutines+held {
		t.Errorf("held %d extra goroutines but the count went %d -> %d; it is not tracking them",
			held, before.Goroutines, during.Goroutines)
	}

	close(release)
	// Let them finish. Gosched rather than a sleep: the goroutines are already
	// runnable, so this is a scheduling point rather than a wait.
	for i := 0; i < 100 && readRuntimeStats().Goroutines > before.Goroutines+held/2; i++ {
		runtime.Gosched()
	}

	// And the heap figures are real numbers rather than zero.
	if during.HeapBytes == 0 || during.HeapObjects == 0 {
		t.Errorf("heap figures came back as %d bytes / %d objects; something is not being read",
			during.HeapBytes, during.HeapObjects)
	}
}

// The descriptor count is a diagnostic, so it degrades rather than failing: a
// platform without /proc/self/fd reports nothing instead of breaking the
// snapshot. Where it does work it must not count the handle it used to look.
func TestTheOpenFileCountIsHonestOrAbsent(t *testing.T) {
	n := openFileCount()
	if n == 0 {
		t.Skip("this platform does not expose /proc/self/fd")
	}

	// Opening a file must move it by one.
	f, err := openTempFile(t)
	if err != nil {
		t.Skipf("cannot open a file here: %v", err)
	}
	after := openFileCount()
	if after != n+1 {
		t.Errorf("opened one file and the count went %d -> %d, want %d", n, after, n+1)
	}
	f.Close()
}

func openTempFile(t *testing.T) (*os.File, error) {
	t.Helper()
	return os.CreateTemp(t.TempDir(), "fdcount")
}
