package webui

import (
	"testing"
	"time"

	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/metrics"
)

// The rate history grows with nobody polling.
//
// Before, the only thing that fed it was /api/tunnels, so a card opened after
// an hour away started from nothing and its first point was an hour's
// average. sampleRates is what the panel runs on its own clock; three new
// snapshots with no request in between must leave two rate points behind.
func TestRateHistoryFillsWithoutAPoll(t *testing.T) {
	const name = "sampler-test"
	rates.mu.Lock()
	delete(rates.last, name)
	delete(rates.hist, name)
	rates.mu.Unlock()

	list := func() []manage.Tunnel { return []manage.Tunnel{{Name: name}} }
	at := time.Unix(1790000000, 0)
	var in uint64
	read := func(string) (metrics.Snapshot, error) {
		return metrics.Snapshot{Name: name, Taken: at, BytesIn: in, BytesOut: in / 2}, nil
	}

	for i := 0; i < 3; i++ {
		sampleRates(list, read)
		// The same snapshot seen twice is not a second reading.
		sampleRates(list, read)
		at = at.Add(30 * time.Second)
		in += 30 << 20
	}

	rates.mu.Lock()
	got := append([]RatePoint(nil), rates.hist[name]...)
	rates.mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("got %d rate points, want 2 — the history is not being kept between polls", len(got))
	}
	if want := float64(30<<20) / 30; got[1].In != want {
		t.Errorf("rate in = %v, want %v", got[1].In, want)
	}
}
