// Package tunhist keeps the long view of every tunnel: traffic and up/down
// state, sampled on a timer and kept for a month.
//
// The panel's sparkline answers "what is happening right now"; this answers
// "what happened this week" — how much a tunnel carried per day, and what
// fraction of the time it was actually up. The sampler runs inside
// backpack-monitor, the process that is always on; the panel only reads.
//
// Two resolutions bound the file: five-minute samples for the last day, and
// hourly buckets for the last thirty. Byte counts are stored cumulative, the
// way the metrics report them, so a gap in sampling loses resolution rather
// than inventing traffic.
package tunhist

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/metrics"
)

const (
	// SampleEvery is the sampler's cadence. Five minutes keeps the day view
	// honest without the file ever mattering on disk.
	SampleEvery = 5 * time.Minute

	keepRecent = 288 // 5-min samples in 24h
	keepHourly = 720 // hours in 30 days
)

// Sample is one five-minute reading: cumulative bytes and whether the tunnel
// was up at that moment.
type Sample struct {
	T   int64  `json:"t"` // unix seconds
	In  uint64 `json:"in"`
	Out uint64 `json:"out"`
	Up  bool   `json:"up"`
}

// Hour is one hourly bucket. Bytes stay cumulative (the value at the end of
// the bucket); UpN of N five-minute checks saw the tunnel up, which is what
// an honest uptime percentage is made of.
type Hour struct {
	T   int64  `json:"t"` // start of the hour, unix seconds
	In  uint64 `json:"in"`
	Out uint64 `json:"out"`
	UpN int    `json:"upN"`
	N   int    `json:"n"`
}

// History is everything kept for one tunnel.
type History struct {
	Recent []Sample `json:"recent"`
	Hourly []Hour   `json:"hourly"`
}

// File is the whole store: one history per tunnel name.
type File struct {
	Updated time.Time           `json:"updated"`
	Tunnels map[string]*History `json:"tunnels"`
}

// Dir is where the file lives; a variable so tests can point it elsewhere.
var Dir = app.ConfigDir

func path() string { return filepath.Join(Dir, "history.json") }

// Load reads the store; missing means empty, nothing has been sampled yet.
func Load() File {
	f := File{Tunnels: map[string]*History{}}
	data, err := os.ReadFile(path())
	if err != nil {
		return f
	}
	_ = json.Unmarshal(data, &f)
	if f.Tunnels == nil {
		f.Tunnels = map[string]*History{}
	}
	return f
}

func save(f File) {
	data, err := json.Marshal(f)
	if err != nil {
		return
	}
	// Atomic, because the panel reads this while the monitor writes it.
	_ = app.WriteFileAtomic(path(), data, 0o644)
}

// Record adds one reading for a tunnel to the store held in f.
func (f *File) Record(name string, in, out uint64, up bool, now time.Time) {
	h := f.Tunnels[name]
	if h == nil {
		h = &History{}
		f.Tunnels[name] = h
	}

	h.Recent = append(h.Recent, Sample{T: now.Unix(), In: in, Out: out, Up: up})
	if len(h.Recent) > keepRecent {
		h.Recent = h.Recent[len(h.Recent)-keepRecent:]
	}

	hourStart := now.Truncate(time.Hour).Unix()
	if n := len(h.Hourly); n > 0 && h.Hourly[n-1].T == hourStart {
		b := &h.Hourly[n-1]
		b.In, b.Out = in, out // cumulative: the bucket ends at the newest value
		b.N++
		if up {
			b.UpN++
		}
	} else {
		b := Hour{T: hourStart, In: in, Out: out, N: 1}
		if up {
			b.UpN = 1
		}
		h.Hourly = append(h.Hourly, b)
		if len(h.Hourly) > keepHourly {
			h.Hourly = h.Hourly[len(h.Hourly)-keepHourly:]
		}
	}
}

// prune drops tunnels that no longer exist, so a deleted tunnel does not
// haunt the file forever.
func (f *File) prune(current map[string]bool) {
	for name := range f.Tunnels {
		if !current[name] {
			delete(f.Tunnels, name)
		}
	}
}

// Run samples every tunnel until ctx is cancelled. It runs in the monitor
// service — the process trusted to always be on — and takes one pass
// immediately so a fresh install has data within minutes, not an hour.
func Run(ctx context.Context) {
	samplePass()
	t := time.NewTicker(SampleEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			samplePass()
		}
	}
}

func samplePass() {
	f := Load()
	now := time.Now()
	current := map[string]bool{}
	for _, t := range manage.List() {
		current[t.Name] = true
		var in, out uint64
		if snap, err := metrics.Read(app.ConfigDir, t.Name); err == nil {
			in, out = snap.BytesIn, snap.BytesOut
		}
		h := manage.TunnelHealth(t)
		f.Record(t.Name, in, out, h.Active && h.Connected, now)
	}
	f.prune(current)
	f.Updated = now
	save(f)
}
