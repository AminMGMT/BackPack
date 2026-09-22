package webui

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/backpack/backpack/internal/alerthist"
	"github.com/backpack/backpack/internal/control"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/node"
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

// linkTestResult is manage's, so a measurement taken here and one taken on a
// managed server are the same shape all the way to the browser.
type linkTestResult = manage.LinkTestResult

// jobLinkTest is the kind this runs under. One at a time, because two path
// measurements at once measure each other — which is what control.Jobs gives
// for free and what the mutex, bool and pointer that used to live here gave by
// hand, without progress, cancellation or any memory of the last one.
const jobLinkTest = "linktest"

func (s *server) handleLinkTest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// "What is happening, or what just happened" — the shape the old
		// struct could not express, because it kept one result and forgot it
		// the moment the next test started.
		job, ok := s.jobs.Latest(jobLinkTest)
		if !ok {
			writeJSON(w, map[string]any{"running": false})
			return
		}
		out := map[string]any{"running": job.State == control.Running, "name": job.Target}
		if res, isResult := control.ResultOf[*linkTestResult](job); isResult {
			out["result"] = res
		}
		if job.Err != "" {
			out["error"] = job.Err
		}
		writeJSON(w, out)

	case http.MethodPost:
		name := r.URL.Query().Get("name")
		t, ok := findTunnel(name)
		if !ok {
			http.Error(w, "unknown tunnel", http.StatusBadRequest)
			return
		}
		// The measurement is taken where the dialling happens.
		//
		// On this machine that is a client-role tunnel. A server-role one has
		// no address to probe from here — but its other half does, and when
		// that half is on a server this panel manages, the panel asks it
		// rather than declining. Refusing while holding a shell on the machine
		// that could answer is the shape this used to have.
		runOn := ""
		if can, why := manage.LinkTestable(t); !can {
			pair, paired := manage.PairFor(name)
			hub := s.nodes.Runner()
			switch {
			case t.Role == "client":
				// Datagram, or another reason no machine can help with.
				http.Error(w, why, http.StatusBadRequest)
				return
			case !paired:
				// Named in a header as well as in the sentence.
				//
				// The panel offers the fix where the refusal happened, and to do
				// that it has to recognise this one refusal among all of them.
				// Matching the words would be the same mistake as keying on
				// "not installed" was: the prose is written for a person, and
				// rewording it must not silently take the button away.
				w.Header().Set(fixHeader, fixLinkTunnel)
				http.Error(w, why+". Link this tunnel to the server holding its other "+
					"end and the panel can run it there.", http.StatusBadRequest)
				return
			case hub == nil || !hub.IsOnline(pair.Node):
				http.Error(w, why+", and "+pair.Node+", which holds the end that does, "+
					"could not be reached.", http.StatusBadGateway)
				return
			default:
				runOn = pair.Node
			}
		}

		peerName := ""
		if runOn != "" {
			if p, ok := manage.PairFor(name); ok {
				peerName = p.PeerName
			}
			if peerName == "" {
				peerName = name
			}
		}

		// The measurement outlives the request that asked for it, which is the
		// whole reason it is a job: a bad path takes longer to measure than a
		// good one, and the worst case — a dead peer — is longer than the
		// panel's write timeout.
		_, err := s.jobs.Start(r.Context(), jobLinkTest, name,
			func(ctx context.Context, p *control.Progress) (any, error) {
				var res linkTestResult
				if runOn == "" {
					p.Step("measuring from this server")
					res = manage.MeasureLink(t)
				} else {
					// Taken on the machine that dials, and labelled with it: a
					// latency figure without the place it was measured from is
					// a number about somebody else's path.
					p.Step("measuring from %s", runOn)
					if err := s.nodes.Runner().Call(runOn, node.OpLinkTest,
						node.NameRequest{Name: peerName}, &res); err != nil {
						res = linkTestResult{Name: name, Error: err.Error()}
					}
					res.Name, res.RanOn = name, runOn
				}
				noteJob(nil)
				return &res, nil
			})
		var busy control.ErrBusy
		if errors.As(err, &busy) {
			writeJSON(w, map[string]string{"status": "already running"})
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"status": "started"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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

	// Daily totals from the hourly buckets.
	//
	// A week by default, because that is what every existing caller draws. The
	// store keeps a month (tunhist.keepHourly is 720 hours), so a caller that
	// wants more can ask with ?days=; anything outside 1..30 is clamped rather
	// than refused, since a bad number here is worth a shorter chart, not an
	// error page.
	type day struct {
		Label string `json:"label"` // "Mon 21"
		In    uint64 `json:"in"`
		Out   uint64 `json:"out"`
	}
	span := 7
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v > 0 {
		span = min(v, 30)
	}
	days := map[string]*day{}
	var order []string
	// The uptime figures below keep their own week-long window whatever the
	// chart asks for: uptime7d has to keep meaning seven days.
	weekAgo := time.Now().AddDate(0, 0, -7).Unix()
	from := time.Now().AddDate(0, 0, -span).Unix()
	for i := 1; i < len(h.Hourly); i++ {
		a, b := h.Hourly[i-1], h.Hourly[i]
		if b.T < from || b.In < a.In || b.Out < a.Out {
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

	// When the configuration changed, so the chart can mark it.
	//
	// A speed chart with no idea what was done to the tunnel makes "did that
	// change help?" a matter of impression. With the moments marked it is a
	// line on a graph, which is the difference between tuning and superstition.
	// Only the ones inside the window the chart covers are sent.
	var changes []int64
	if len(series) > 0 {
		from := series[0].T
		for _, at := range manage.ConfigChangeTimes(name) {
			if at >= from {
				changes = append(changes, at)
			}
		}
	}

	writeJSON(w, map[string]any{
		"series": series, "days": dayList,
		"uptime24h": up24, "uptime7d": up7,
		"changes": changes,
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

// What the panel can offer to do about a refusal.
//
// A refusal that has one specific remedy says so in a header, so the page can
// put that remedy in front of the operator instead of a sentence describing it.
// The sentence stays — it is what somebody reading a log or a curl sees.
const (
	fixHeader     = "X-Backpack-Fix"
	fixLinkTunnel = "link-tunnel"
)
