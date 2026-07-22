package webui

import (
	"net/http"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/alerthist"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/tunhist"
)

// The read-only monitoring endpoints: health checks, alert history and the
// link test. All three surface things the CLI already computes — the panel
// adds no judgement of its own, so the two can never disagree.

// handleHealth runs the full diagnostic pass and returns every check. It is
// invoked when the Health modal opens, not on a timer — the pass makes real
// TCP probes and takes a few seconds.
func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	checks := manage.Diagnose()
	out := make([]map[string]string, len(checks))
	for i, c := range checks {
		out[i] = map[string]string{
			"group":  c.Group,
			"name":   c.Name,
			"level":  healthLevel(c.Level),
			"detail": c.Detail,
			"fix":    c.Fix,
		}
	}
	writeJSON(w, out)
}

func healthLevel(l manage.CheckLevel) string {
	switch l {
	case manage.CheckOK:
		return "ok"
	case manage.CheckWarn:
		return "warn"
	case manage.CheckFail:
		return "fail"
	default:
		return "info"
	}
}

// handleAlerts returns what the monitor's alert watcher has recorded: the
// conditions active right now and the recent messages. The panel only reads;
// the watcher in backpack-monitor writes.
func (s *server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, alerthist.Load())
}

// --- link test ---------------------------------------------------------------

// A probe takes ~10 seconds against a live server and up to a minute against a
// dead one — longer than the HTTP write timeout. So the test runs as a job:
// POST starts it, GET polls for the outcome. One at a time is plenty; the
// numbers are only meaningful when the probes are not competing.

type linkTestResult struct {
	Name     string   `json:"name"`
	Target   string   `json:"target"`
	Sent     int      `json:"sent"`
	Received int      `json:"received"`
	MinMs    int      `json:"minMs"`
	AvgMs    int      `json:"avgMs"`
	MaxMs    int      `json:"maxMs"`
	JitterMs int      `json:"jitterMs"`
	LossPct  float64  `json:"lossPct"`
	Usable   bool     `json:"usable"`
	Error    string   `json:"error,omitempty"`
	RecLabel string   `json:"recLabel,omitempty"`
	RecWhy   []string `json:"recWhy,omitempty"`
	Caveats  []string `json:"caveats,omitempty"`
}

var linkTest = struct {
	mu      sync.Mutex
	running bool
	name    string
	result  *linkTestResult
}{}

func (s *server) handleLinkTest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		linkTest.mu.Lock()
		running, name, res := linkTest.running, linkTest.name, linkTest.result
		linkTest.mu.Unlock()
		writeJSON(w, map[string]any{"running": running, "name": name, "result": res})

	case http.MethodPost:
		name := r.URL.Query().Get("name")
		t, ok := findTunnel(name)
		if !ok {
			http.Error(w, "unknown tunnel", http.StatusBadRequest)
			return
		}
		// The link is measured from the side that dials out. A server tunnel
		// has no address to probe, and probing a datagram tunnel's port over
		// TCP would report a working tunnel as dead.
		if t.Role != "client" {
			http.Error(w, "the link test runs on the client (kharej) side — it is the side that dials out", http.StatusBadRequest)
			return
		}
		if manage.IsDatagram(t.Transport) {
			http.Error(w, "a UDP-based tunnel cannot be probed over TCP — its metrics (loss, FEC repairs) are the honest measure of this link", http.StatusBadRequest)
			return
		}

		linkTest.mu.Lock()
		if linkTest.running {
			linkTest.mu.Unlock()
			writeJSON(w, map[string]string{"status": "already running"})
			return
		}
		linkTest.running, linkTest.name, linkTest.result = true, name, nil
		linkTest.mu.Unlock()

		go func() {
			res := runLinkTest(t)
			linkTest.mu.Lock()
			linkTest.running, linkTest.result = false, &res
			linkTest.mu.Unlock()
		}()
		writeJSON(w, map[string]string{"status": "started"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func runLinkTest(t manage.Tunnel) linkTestResult {
	q := manage.ProbePath(t.Addr)
	res := linkTestResult{
		Name:     t.Name,
		Target:   q.Target,
		Sent:     q.Sent,
		Received: q.Received,
		MinMs:    int(q.Min / time.Millisecond),
		AvgMs:    int(q.Avg / time.Millisecond),
		MaxMs:    int(q.Max / time.Millisecond),
		JitterMs: int(q.Jitter / time.Millisecond),
		LossPct:  q.LossPercent(),
		Usable:   q.Usable(),
	}
	if q.Err != nil {
		res.Error = q.Err.Error()
		return res
	}
	rec := manage.RecommendTransport(q, t.Transport)
	res.RecLabel = rec.Label
	res.RecWhy = rec.Why
	res.Caveats = rec.Caveats
	return res
}

// --- long-term history -------------------------------------------------------

// handleHistory serves the derived views of a tunnel's sampled history: speed
// over the last day, per-day totals for the week, and uptime percentages.
// The raw file stores cumulative counters; everything the chart needs is
// computed here so the page stays simple.
func (s *server) handleHistory(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	f := tunhist.Load()
	h := f.Tunnels[name]
	if h == nil || len(h.Recent) < 2 {
		writeJSON(w, map[string]any{"collecting": true})
		return
	}

	// Speed series: bytes/second between consecutive 5-minute samples. A
	// counter that went backwards (a restore) yields no point, not nonsense.
	type pt struct {
		T   int64   `json:"t"`
		In  float64 `json:"in"`
		Out float64 `json:"out"`
	}
	var series []pt
	for i := 1; i < len(h.Recent); i++ {
		a, b := h.Recent[i-1], h.Recent[i]
		secs := float64(b.T - a.T)
		if secs <= 0 || b.In < a.In || b.Out < a.Out {
			continue
		}
		series = append(series, pt{T: b.T,
			In:  float64(b.In-a.In) / secs,
			Out: float64(b.Out-a.Out) / secs})
	}

	// Daily totals for the last 7 days, from the hourly buckets.
	type day struct {
		Label string `json:"label"` // "Mon 21"
		In    uint64 `json:"in"`
		Out   uint64 `json:"out"`
	}
	days := map[string]*day{}
	var order []string
	weekAgo := time.Now().AddDate(0, 0, -7).Unix()
	for i := 1; i < len(h.Hourly); i++ {
		a, b := h.Hourly[i-1], h.Hourly[i]
		if b.T < weekAgo || b.In < a.In || b.Out < a.Out {
			continue
		}
		label := time.Unix(b.T, 0).Format("Mon 2")
		d := days[label]
		if d == nil {
			d = &day{Label: label}
			days[label] = d
			order = append(order, label)
		}
		d.In += b.In - a.In
		d.Out += b.Out - a.Out
	}
	dayList := make([]day, 0, len(order))
	for _, label := range order {
		dayList = append(dayList, *days[label])
	}

	// Uptime: the day from the 5-minute samples, the week from the hourly
	// up-counts — each computed from what was actually observed.
	up24 := -1.0
	if n := len(h.Recent); n > 0 {
		up := 0
		for _, s := range h.Recent {
			if s.Up {
				up++
			}
		}
		up24 = float64(up) / float64(n) * 100
	}
	up7 := -1.0
	var upN, n int
	for _, b := range h.Hourly {
		if b.T >= weekAgo {
			upN += b.UpN
			n += b.N
		}
	}
	if n > 0 {
		up7 = float64(upN) / float64(n) * 100
	}

	writeJSON(w, map[string]any{
		"series": series, "days": dayList,
		"uptime24h": up24, "uptime7d": up7,
	})
}

func findTunnel(name string) (manage.Tunnel, bool) {
	for _, t := range manage.List() {
		if t.Name == name {
			return t, true
		}
	}
	return manage.Tunnel{}, false
}
