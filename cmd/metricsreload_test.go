package cmd

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/backpack/backpack/internal/metrics"
)

// A reload runs the next generation's collector in the same process, straight
// after the last one stopped. The new one takes its baseline from the file, so
// the last one's final write has to be in by then, and what the process had
// already counted must not be added a second time. Either way round, the
// panel's all-time figure was wrong after every config edit.
func TestAReloadCarriesTheTrafficTotalOverExactly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nl.toml")
	ctx := context.Background()

	// A fresh tunnel counts from zero: the collector takes only what the
	// process counts after it starts, so whatever earlier tests in this process
	// added to the shared counters is not this tunnel's. Comparing against
	// those counters made the test pass only when it ran first.
	var in0, out0 uint64

	stop := startMetrics(ctx, path, "tcp", "server")
	metrics.AddBytes(1000, 400)
	stop() // the generation ends: its last write must be in when this returns
	snap, err := metrics.Read(dir, "nl")
	if err != nil {
		t.Fatal(err)
	}
	if snap.BytesIn != in0+1000 || snap.BytesOut != out0+400 {
		t.Fatalf("after the first generation: in %d, out %d; want %d/%d",
			snap.BytesIn, snap.BytesOut, in0+1000, out0+400)
	}

	stop = startMetrics(ctx, path, "tcp", "server")
	metrics.AddBytes(10, 5)
	stop()
	snap, err = metrics.Read(dir, "nl")
	if err != nil {
		t.Fatal(err)
	}
	if snap.BytesIn != in0+1010 || snap.BytesOut != out0+405 {
		t.Errorf("after a reload: in %d, out %d; want %d/%d",
			snap.BytesIn, snap.BytesOut, in0+1010, out0+405)
	}
}
