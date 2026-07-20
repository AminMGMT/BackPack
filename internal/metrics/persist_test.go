package metrics

import (
	"os"
	"testing"
)

// Traffic totals have to survive a restart. A tunnel restarts for all sorts of
// ordinary reasons — an update, a config edit, the watchdog — and a total that
// quietly returns to zero each time is not a total of anything.

// resetCounters clears the process-wide counters so one test cannot see
// another's traffic.
func resetCounters(t *testing.T) {
	t.Helper()
	bytesIn.Store(0)
	bytesOut.Store(0)
	t.Cleanup(func() {
		bytesIn.Store(0)
		bytesOut.Store(0)
	})
}

func TestTrafficResumesAfterRestart(t *testing.T) {
	resetCounters(t)
	dir := t.TempDir()

	// First run carries some traffic and writes it out.
	first := NewCollector(dir, "t", "tcp", "server", nil, nil)
	AddBytes(1000, 500)
	if err := first.Write(); err != nil {
		t.Fatal(err)
	}

	// Restart: the process counters start again from zero.
	bytesIn.Store(0)
	bytesOut.Store(0)

	second := NewCollector(dir, "t", "tcp", "server", nil, nil)
	got := second.Snapshot()
	if got.BytesIn != 1000 || got.BytesOut != 500 {
		t.Fatalf("after a restart: in %d, out %d; want the previous 1000/500",
			got.BytesIn, got.BytesOut)
	}

	// New traffic adds to the old total rather than replacing it.
	AddBytes(200, 100)
	got = second.Snapshot()
	if got.BytesIn != 1200 || got.BytesOut != 600 {
		t.Errorf("in %d, out %d; want 1200/600 (previous plus new)", got.BytesIn, got.BytesOut)
	}
}

// Restoring a backup brings the metrics file with it, so the totals should
// continue from what the backup held rather than starting over.
func TestTrafficContinuesFromARestoredBackup(t *testing.T) {
	resetCounters(t)
	dir := t.TempDir()

	// Stand in for a restored file by writing one before the collector exists.
	seed := NewCollector(dir, "t", "kcp", "server", nil, nil)
	AddBytes(9_000_000, 3_000_000)
	if err := seed.Write(); err != nil {
		t.Fatal(err)
	}

	bytesIn.Store(0)
	bytesOut.Store(0)

	restored := NewCollector(dir, "t", "kcp", "server", nil, nil)
	got := restored.Snapshot()
	if got.BytesIn != 9_000_000 || got.BytesOut != 3_000_000 {
		t.Fatalf("restored totals: in %d, out %d; want 9000000/3000000",
			got.BytesIn, got.BytesOut)
	}
}

// A tunnel that has never run has nothing to carry forward, and must not fail
// or invent a number.
func TestNoBaselineWhenThereIsNoHistory(t *testing.T) {
	resetCounters(t)

	c := NewCollector(t.TempDir(), "brand-new", "tcp", "server", nil, nil)
	if got := c.Snapshot(); got.BytesIn != 0 || got.BytesOut != 0 {
		t.Errorf("a fresh tunnel reported in %d, out %d; want zero", got.BytesIn, got.BytesOut)
	}
}

// Each tunnel keeps its own history; one must not inherit another's.
func TestBaselinesArePerTunnel(t *testing.T) {
	resetCounters(t)
	dir := t.TempDir()

	a := NewCollector(dir, "alpha", "tcp", "server", nil, nil)
	AddBytes(5000, 5000)
	if err := a.Write(); err != nil {
		t.Fatal(err)
	}

	bytesIn.Store(0)
	bytesOut.Store(0)

	b := NewCollector(dir, "beta", "tcp", "server", nil, nil)
	if got := b.Snapshot(); got.BytesIn != 0 {
		t.Errorf("beta picked up alpha's %d bytes", got.BytesIn)
	}
}

// A corrupt or truncated file must not take the tunnel's counters with it.
func TestCorruptHistoryIsIgnored(t *testing.T) {
	resetCounters(t)
	dir := t.TempDir()

	if err := writeRaw(Path(dir, "t"), "{not json at all"); err != nil {
		t.Fatal(err)
	}
	c := NewCollector(dir, "t", "tcp", "server", nil, nil)
	AddBytes(100, 50)

	if got := c.Snapshot(); got.BytesIn != 100 || got.BytesOut != 50 {
		t.Errorf("in %d, out %d; a corrupt history should count as no history", got.BytesIn, got.BytesOut)
	}
}

func writeRaw(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}
