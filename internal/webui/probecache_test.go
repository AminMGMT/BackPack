package webui

import (
	"sync/atomic"
	"testing"
	"time"
)

// A card whose far end is down used to cost every poll the whole probe
// timeout. Only the first answer for a target is waited for; after that the
// poll gets the last answer at once and the probe is renewed behind it.
func TestAProbeIsWaitedForOnceAndRenewedBehindThePoll(t *testing.T) {
	var runs atomic.Int32
	slow := func() int { runs.Add(1); time.Sleep(200 * time.Millisecond); return 42 }
	key := "test\x00" + t.Name()

	if v := cachedProbe(key, slow); v != 42 {
		t.Fatalf("first answer %d", v)
	}
	// Made old, so the next call renews it — without waiting.
	probeMu.Lock()
	probes[key].at = time.Now().Add(-time.Minute)
	probeMu.Unlock()
	start := time.Now()
	if v := cachedProbe(key, slow); v != 42 {
		t.Fatalf("second answer %d", v)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("the poll waited %s for a renewal", d)
	}
	cachedProbe(key, slow) // a renewal is already running; no second one
	time.Sleep(300 * time.Millisecond)
	if n := runs.Load(); n != 2 {
		t.Errorf("the probe ran %d times, want 2", n)
	}
}
