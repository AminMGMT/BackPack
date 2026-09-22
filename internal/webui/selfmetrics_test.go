package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The panel's own numbers.
//
// /metrics reported the tunnels and the host and said nothing about the process
// serving it — the component an operator reaches for when something is wrong,
// and the one thing on the box with no numbers at all.

func TestRequestsAreCountedByOutcome(t *testing.T) {
	p := &panelStats{started: time.Now()}
	p.observe(200, 5*time.Millisecond)
	p.observe(204, time.Millisecond)
	p.observe(401, time.Millisecond)
	p.observe(429, time.Millisecond)
	p.observe(500, 50*time.Millisecond)

	if got := p.ok.Load(); got != 2 {
		t.Errorf("ok = %d, want 2", got)
	}
	// A rising count of these on the token endpoints is somebody guessing.
	if got := p.refused.Load(); got != 2 {
		t.Errorf("refused = %d, want 2", got)
	}
	// And a rising count of these is this program being wrong, which is a
	// different problem with a different owner.
	if got := p.failed.Load(); got != 1 {
		t.Errorf("failed = %d, want 1", got)
	}
	if got := p.requests.Load(); got != 5 {
		t.Errorf("requests = %d, want 5", got)
	}
}

func TestTheSlowestRequestIsRemembered(t *testing.T) {
	p := &panelStats{started: time.Now()}
	p.observe(200, 5*time.Millisecond)
	p.observe(200, 200*time.Millisecond)
	p.observe(200, time.Millisecond)

	if got := time.Duration(p.maxNanos.Load()); got != 200*time.Millisecond {
		t.Fatalf("slowest = %v, want 200ms — a panel that has become slow shows "+
			"up here before anybody complains", got)
	}
}

// A request refused for a bad credential is exactly the one worth counting, so
// the instrumentation has to sit outside the authorisation rather than inside
// it.
func TestARefusedRequestIsStillCounted(t *testing.T) {
	isolateAccess(t)
	before := panelMetrics.refused.Load()

	s := &server{sessions: newSessionStore()}
	var ran bool
	w := httptest.NewRecorder()
	s.guard(ScopeRead, ok(&ran))(w, req("GET", "/api/stats", ""))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("setup: status %d", w.Code)
	}
	if ran {
		t.Fatal("setup: the handler ran")
	}
	if got := panelMetrics.refused.Load(); got != before+1 {
		t.Fatalf("refused went %d → %d; a request turned away at the door was not "+
			"counted, which is the one most worth knowing about", before, got)
	}
}

// The page has to carry them, with the outcome as a label rather than three
// separate metric names.
func TestThePanelMetricsAppearOnThePage(t *testing.T) {
	var b strings.Builder
	writePanelMetrics(&b)
	page := b.String()

	for _, want := range []string{
		"backpack_panel_uptime_seconds",
		`backpack_panel_requests_total{outcome="ok"}`,
		`backpack_panel_requests_total{outcome="refused"}`,
		`backpack_panel_requests_total{outcome="failed"}`,
		"backpack_panel_request_ms_mean",
		"backpack_panel_request_ms_max",
		`backpack_panel_jobs_total{outcome="done"}`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page is missing %q", want)
		}
	}

	// No per-path labels. The panel is served under a secret base path on a
	// port that gets scanned, so a label per URL is a metric that grows with
	// the number of paths anybody has ever probed.
	if strings.Contains(page, `path="`) {
		t.Error("a per-path label appeared; on a scanned port that is unbounded cardinality")
	}
}

// A counter that goes down is one Prometheus reads as a restart, so the job
// counts must not be derived from the bounded job history.
func TestJobCountsOnlyGoUp(t *testing.T) {
	before := func() (int64, int64) {
		jobsMu.Lock()
		defer jobsMu.Unlock()
		return jobsDone, jobsFailed
	}
	d0, f0 := before()

	noteJobOutcome(true)
	noteJobOutcome(true)
	noteJobOutcome(false)

	d1, f1 := before()
	if d1 != d0+2 || f1 != f0+1 {
		t.Fatalf("counts went (%d,%d) → (%d,%d)", d0, f0, d1, f1)
	}
}
