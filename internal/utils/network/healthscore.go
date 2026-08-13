package network

import (
	"context"
	"net"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// Health scoring for a multi-exit tunnel.
//
// A rotating endpoint list already fails over when a dial fails outright, but a
// route rarely dies cleanly — far more often it just gets worse: latency climbs,
// jitter sets in, a few percent of packets start dropping. None of that trips a
// failed dial, so without scoring the tunnel stays on a degrading exit until it
// finally breaks. This measures every exit and steers traffic to the healthiest.
//
// The score is the one a real-time link is felt through, not raw latency:
//
//	score = mean_rtt_ms + 2·jitter_ms + 20·loss_percent      (lower is better)
//
// Jitter and loss are weighted far above latency because a steady 90 ms exit
// beats a 60 ms one that stutters — a stutter is a lost frame, a spike in latency
// is not. It is the same weighting hydra proved on these routes.

// EndpointScore is one exit's measured health.
type EndpointScore struct {
	Addr      string
	RTTms     float64
	JitterMs  float64
	LossPct   float64
	Score     float64
	Reachable bool
}

// scoreWeights: jitter counts double, each percent of loss counts twenty times a
// millisecond of latency.
func computeScore(rttMs, jitterMs, lossPct float64) float64 {
	return rttMs + 2*jitterMs + 20*lossPct
}

var (
	// Linux `ping -q` summary: "rtt min/avg/max/mdev = 12.3/14.1/40.2/3.4 ms".
	reScoreRTT  = regexp.MustCompile(`=\s*[0-9.]+/([0-9.]+)/[0-9.]+/([0-9.]+)`)
	reScoreLoss = regexp.MustCompile(`([0-9.]+)% packet loss`)
)

// hostOf strips the port so ICMP has a bare host to ping; an address with no
// port is returned unchanged.
func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// parseScorePing pulls avg RTT, jitter (ping's mdev) and loss out of a ping
// summary. Split out from the exec so it can be tested without a network.
func parseScorePing(out string) EndpointScore {
	var s EndpointScore
	if m := reScoreLoss.FindStringSubmatch(out); m != nil {
		s.LossPct, _ = strconv.ParseFloat(m[1], 64)
	} else {
		s.LossPct = 100
	}
	if m := reScoreRTT.FindStringSubmatch(out); m != nil {
		s.RTTms, _ = strconv.ParseFloat(m[1], 64)
		s.JitterMs, _ = strconv.ParseFloat(m[2], 64)
		s.Reachable = true
	}
	s.Score = computeScore(s.RTTms, s.JitterMs, s.LossPct)
	return s
}

// ScoreEndpoint measures one exit with a short ICMP burst. An unreachable exit
// comes back with Reachable=false and a score that sorts it last.
func ScoreEndpoint(addr string, samples int) EndpointScore {
	if samples <= 0 {
		samples = 5
	}
	cmd := exec.Command("ping", "-c", strconv.Itoa(samples), "-i", "0.2", "-W", "2", "-q", hostOf(addr))
	out, _ := cmd.CombinedOutput() // non-zero exit on loss is expected; parse anyway
	s := parseScorePing(string(out))
	s.Addr = addr
	if !s.Reachable {
		s.Score = 1e9 // last, but still comparable
	}
	return s
}

// ScoreEndpoints measures every exit and returns them best-first. Handy for the
// operator-facing "which exit should I pin?" view.
func ScoreEndpoints(addrs []string, samples int) []EndpointScore {
	out := make([]EndpointScore, len(addrs))
	for i, a := range addrs {
		out[i] = ScoreEndpoint(a, samples)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score < out[j].Score })
	return out
}

// EnableHealthSteering starts a background loop that scores every endpoint on
// `interval` and steers Current()/Next() to the healthiest, with hysteresis so
// the choice does not flap. It no-ops on a single endpoint, and it turns spread
// off because steering and spreading are opposite intentions. The loop stops
// when ctx is done. `log`, if non-nil, is called with a one-line note whenever
// the preferred exit actually changes.
func (e *Endpoints) EnableHealthSteering(ctx context.Context, interval time.Duration, log func(string)) {
	if e == nil || len(e.list) <= 1 {
		return
	}
	e.spread.Store(false)
	e.preferred.Store(e.idx.Load())
	e.steer.Store(true)
	go e.steerLoop(ctx, interval, log)
}

// steerLoop is the scoring cadence with its hysteresis. A challenger only takes
// over when it is clearly better (≥15%) for several checks in a row, so a single
// bad sample on the current exit cannot bounce traffic away and back. The one
// exception is a preferred exit that has gone unreachable: there is nothing to
// protect, so the switch is immediate.
func (e *Endpoints) steerLoop(ctx context.Context, interval time.Duration, log func(string)) {
	const betterBy = 0.85 // a challenger must score at most 85% of the incumbent
	const needStreak = 3  // ...for this many consecutive checks

	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	candidate := -1
	streak := 0

	for {
		select {
		case <-ctx.Done():
			e.steer.Store(false)
			return
		case <-ticker.C:
		}

		list := e.All()
		scores := make([]EndpointScore, len(list))
		best := -1
		for i, a := range list {
			scores[i] = ScoreEndpoint(a, 5)
			if scores[i].Reachable && (best == -1 || scores[i].Score < scores[best].Score) {
				best = i
			}
		}
		if best == -1 {
			continue // nothing answered; keep whatever was preferred
		}

		cur := e.clampIdx(e.preferred.Load())
		if best == cur {
			candidate, streak = -1, 0
			continue
		}

		curDead := !scores[cur].Reachable
		clearlyBetter := scores[best].Score <= betterBy*scores[cur].Score
		if !curDead && !clearlyBetter {
			candidate, streak = -1, 0
			continue
		}

		if candidate == best {
			streak++
		} else {
			candidate, streak = best, 1
		}
		if curDead || streak >= needStreak {
			e.preferred.Store(int64(best))
			candidate, streak = -1, 0
			if log != nil {
				log("failover: steering to healthiest exit " + list[best] +
					" (score " + strconv.FormatFloat(scores[best].Score, 'f', 0, 64) + ")")
			}
		}
	}
}
