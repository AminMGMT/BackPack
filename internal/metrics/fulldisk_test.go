package metrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// What happens to a tunnel when the disk fills.
//
// This is a real VPS failure and one of the quietest: journald grows, an
// unrotated log grows, a backup lands, and every write on the machine starts
// failing at once. Metrics are written by the engine on a ticker and read by
// everything else — the watchdog's stall detection, the panel, the Telegram
// report — so the question is what a failed write does to the tunnel that is
// still carrying traffic perfectly well.
//
// The answer has to be: nothing, and it has to keep being nothing. A write
// failure must not stop the ticker, must not corrupt what was already written,
// and must not need anybody to restart the tunnel once there is room again.
//
// An unwritable directory is the same failure as a full disk from the writer's
// point of view — the write fails, everything else is untouched — and it is one
// a test can create.

func unwritableDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root, which is not subject to the permission bits this test uses")
	}
	dir := filepath.Join(t.TempDir(), "metrics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("making the directory: %v", err)
	}
	return dir
}

func seal(t *testing.T, dir string)   { t.Helper(); chmod(t, dir, 0o500) }
func unseal(t *testing.T, dir string) { t.Helper(); chmod(t, dir, 0o755) }

func chmod(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func collector(dir, name string) *Collector {
	var in, out uint64 = 1024, 2048
	return NewCollector(dir, name, "tcp", "server",
		func() uint64 { return in }, func() uint64 { return out })
}

// A write that cannot happen is reported and nothing else.
func TestAFailedWriteIsReportedRatherThanFatal(t *testing.T) {
	dir := unwritableDir(t)
	c := collector(dir, "fr-relay")

	if err := c.Write(); err != nil {
		t.Fatalf("the first write should have worked: %v", err)
	}
	seal(t, dir)
	if err := c.Write(); err == nil {
		t.Fatal("a write into a directory that cannot be written reported success")
	}
}

// The snapshot that was already there survives a disk that has filled since.
//
// This is what the tmp-file-and-rename is for, and it matters more when writes
// are failing than when they are working: a reader that finds a truncated or
// missing snapshot while the tunnel is healthy concludes the tunnel is not.
func TestTheLastGoodSnapshotSurvivesAFullDisk(t *testing.T) {
	dir := unwritableDir(t)
	c := collector(dir, "fr-relay")

	if err := c.Write(); err != nil {
		t.Fatalf("the first write should have worked: %v", err)
	}
	before, err := Read(dir, "fr-relay")
	if err != nil {
		t.Fatalf("reading the snapshot back: %v", err)
	}

	seal(t, dir)
	for i := 0; i < 5; i++ {
		_ = c.Write()
	}

	after, err := Read(dir, "fr-relay")
	if err != nil {
		t.Fatalf("the snapshot became unreadable once writes started failing: %v", err)
	}
	if after.BytesIn != before.BytesIn || after.BytesOut != before.BytesOut {
		t.Fatalf("the snapshot changed while every write was failing: %+v then %+v", before, after)
	}
}

// The ticker keeps running through the failures and writes again the moment
// there is room, with nobody restarting anything.
//
// A collector that gave up on the first error would leave a tunnel that is
// carrying traffic looking dead to the watchdog for as long as the process
// lives — which is the failure this whole test file exists for. The recovery is
// the assertion; the failures in the middle are the setup.
func TestTheCollectorWritesAgainWhenThereIsRoom(t *testing.T) {
	dir := unwritableDir(t)
	c := collector(dir, "fr-relay")

	seal(t, dir)

	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() { defer close(stopped); c.Run(done, 20*time.Millisecond) }()
	// Run does a final write on its way out, so the test has to outlive it or
	// t.TempDir's cleanup races that write.
	stop := func() { close(done); <-stopped }

	// Long enough that a collector which stopped on the first error has
	// certainly stopped by now.
	time.Sleep(200 * time.Millisecond)
	if _, err := Read(dir, "fr-relay"); err == nil {
		t.Fatal("a snapshot was written into a directory that cannot be written")
	}

	unseal(t, dir)

	deadline := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		if _, err := Read(dir, "fr-relay"); err == nil {
			stop()
			return
		} else {
			last = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	t.Fatalf("the collector never wrote again after the disk had room: %v", last)
}
