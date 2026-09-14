package sysstat

import (
	"testing"
	"time"
)

// The processor figure a managed server reports has to be measured.
//
// Get uses cpu.Percent with a zero interval, which reports the delta since the
// last call in the same process. That is right for the panel and the monitor,
// which are long-lived and call it on a timer. It is meaningless in `backpack
// node exec` — a process that starts, answers one question and exits — because
// there is no previous call: the only delta available is the one since the
// package initialised microseconds earlier. Over a window that short /proc/stat
// has usually not ticked at all, and gopsutil then returns 0 when nothing moved
// and 100 when a single jiffy landed.
//
// That is why a managed server's card jumped between 0%, 100% and 50% on every
// poll while the machine sat idle. The readings were not noisy; they were
// arithmetic on a window of no length.
//
// So the one thing worth pinning is that this actually waits. A version that
// returns immediately is the bug, whatever number it happens to produce.
func TestCPUPercentOverActuallySamplesAWindow(t *testing.T) {
	const window = 120 * time.Millisecond

	start := time.Now()
	v := CPUPercentOver(window)
	elapsed := time.Since(start)

	// Generous slack under the window: a timer may fire fractionally early, and
	// the failure being guarded against returns in microseconds, not in 100ms.
	if elapsed < window-20*time.Millisecond {
		t.Errorf("returned after %v for a %v window — it did not sample over a window, "+
			"which is the whole reason this exists", elapsed, window)
	}
	if v < 0 || v > 100 {
		t.Errorf("processor reading is %v, which is not a percentage", v)
	}
}

// A caller that asks for nothing still gets a measurement rather than the
// instantaneous reading this was written to avoid.
func TestCPUPercentOverRefusesAZeroWindow(t *testing.T) {
	start := time.Now()
	CPUPercentOver(0)
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("a zero window returned in %v; it has to fall back to a real one, "+
			"or the caller gets exactly the reading this replaces", elapsed)
	}
}
