package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/backpack/backpack/internal/metrics"
)

func TestMetricsShutdownWaitsForFinalSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sigterm.toml")
	ctx, cancel := context.WithCancel(context.Background())
	wait := reverseMetrics(ctx, Request{ConfigPath: path}, "tcp", "client")

	metrics.AddBytes(17, 29)
	cancel()
	wait()

	snapshot, err := metrics.Read(dir, "sigterm")
	if err != nil {
		t.Fatalf("read final metrics snapshot: %v", err)
	}
	if snapshot.BytesIn != 17 || snapshot.BytesOut != 29 {
		t.Fatalf("final counters = %d/%d, want 17/29", snapshot.BytesIn, snapshot.BytesOut)
	}
}
