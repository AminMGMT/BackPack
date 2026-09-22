package webui

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// What the panel is doing, measured by the panel.
//
// /metrics reported the tunnels and the host and said nothing about the process
// serving it. That is the component an operator reaches for when something is
// wrong, it is root on the machine, and it was the one thing on the box with no
// numbers at all — so "the panel is slow" or "the panel is being hammered" had
// no answer but a guess.
//
// Deliberately small. Four things, none of which needs a time series database
// to be useful:
//
//   - how many requests, by outcome, because a rising count of 4xx on the token
//     endpoints is somebody guessing and a rising count of 5xx is this program
//     being wrong;
//   - how long they take, as a running maximum and a total, because a panel
//     that has become slow shows up there before anybody complains;
//   - how many background jobs have run and how many failed;
//   - how long this process has been up, which is the first thing anybody asks
//     and was not being reported either.
//
// No per-path cardinality. A label per URL on a panel with a secret base path
// is a metric that grows with the number of paths anybody has ever probed,
// which on a port that gets scanned is unbounded — and the class of the answer
// is what matters here, not which of forty endpoints served it.

type panelStats struct {
	started time.Time

	requests atomic.Int64
	ok       atomic.Int64 // 2xx and 3xx
	refused  atomic.Int64 // 4xx: unauthorised, forbidden, rate-limited
	failed   atomic.Int64 // 5xx: this program being wrong

	// Duration as a total and a maximum rather than a histogram: a histogram
	// needs buckets chosen in advance, and the two questions actually asked are
	// "is it slower than it was" and "what is the worst case".
	totalNanos atomic.Int64
	maxNanos   atomic.Int64
}

var panelMetrics = &panelStats{started: time.Now()}

func (p *panelStats) observe(status int, took time.Duration) {
	p.requests.Add(1)
	switch {
	case status >= 500:
		p.failed.Add(1)
	case status >= 400:
		p.refused.Add(1)
	default:
		p.ok.Add(1)
	}
	p.totalNanos.Add(int64(took))
	for {
		cur := p.maxNanos.Load()
		if int64(took) <= cur || p.maxNanos.CompareAndSwap(cur, int64(took)) {
			break
		}
	}
}

// instrument wraps a handler so every request is counted.
//
// It sits outside the authorisation guard on purpose: a request refused for a
// bad token is exactly the request worth counting, and one counted only after
// it was allowed through would miss every attempt to get in.
func instrument(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK // handler wrote nothing; net/http sends 200
		}
		panelMetrics.observe(status, time.Since(start))
	}
}

// writePanelMetrics appends the panel's own numbers to a Prometheus page.
func writePanelMetrics(b *strings.Builder) {
	p := panelMetrics

	gauge(b, "backpack_panel_uptime_seconds", "How long this panel process has been running",
		time.Since(p.started).Seconds())

	counterHead(b, "backpack_panel_requests_total", "Requests served, by outcome")
	fmt.Fprintf(b, "backpack_panel_requests_total{outcome=\"ok\"} %d\n", p.ok.Load())
	fmt.Fprintf(b, "backpack_panel_requests_total{outcome=\"refused\"} %d\n", p.refused.Load())
	fmt.Fprintf(b, "backpack_panel_requests_total{outcome=\"failed\"} %d\n", p.failed.Load())

	n := p.requests.Load()
	var mean float64
	if n > 0 {
		mean = float64(p.totalNanos.Load()) / float64(n) / 1e6
	}
	gauge(b, "backpack_panel_request_ms_mean", "Mean time to serve a request, milliseconds", mean)
	gauge(b, "backpack_panel_request_ms_max", "Slowest request since this panel started, milliseconds",
		float64(p.maxNanos.Load())/1e6)

	jobsMu.Lock()
	done, failed := jobsDone, jobsFailed
	jobsMu.Unlock()
	counterHead(b, "backpack_panel_jobs_total", "Background jobs this panel has run, by outcome")
	fmt.Fprintf(b, "backpack_panel_jobs_total{outcome=\"done\"} %d\n", done)
	fmt.Fprintf(b, "backpack_panel_jobs_total{outcome=\"failed\"} %d\n", failed)
}

// Job outcomes, counted where they are decided.
//
// control.Jobs keeps a bounded history and the panel reports from it, so a
// count read from that history would fall as old jobs age out — a counter that
// goes down is a counter Prometheus reads as a restart.
var (
	jobsMu     sync.Mutex
	jobsDone   int64
	jobsFailed int64
)

func noteJobOutcome(ok bool) {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	if ok {
		jobsDone++
		return
	}
	jobsFailed++
}
