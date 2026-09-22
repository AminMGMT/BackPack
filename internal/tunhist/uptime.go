package tunhist

import "time"

// Uptime, from the samples already on disk.
//
// Hour carries UpN of N five-minute checks, and the comment on it has said
// since it was written that this is what an honest uptime percentage is made
// of. Nothing turned it into one. "This tunnel was up 99.2% of last month" is
// the number an operator is asked for by whoever is paying them, and every
// piece of it was already being recorded.
//
// # Not measured is not down
//
// This is the whole care in the function. A tunnel created yesterday was not
// down for the twenty-nine days before it existed, and reporting it as 3%
// uptime is worse than reporting nothing, because 3% is a number somebody will
// act on. Same for an hour in which no check ran: it contributes nothing rather
// than counting as an hour of failure.
//
// So the answer is a percentage *and* the number of checks behind it, and an ok
// that is false when there is nothing to go on. A caller that ignores the count
// is publishing a figure without its sample size, which for a young tunnel is
// how "100% uptime" comes to mean "we looked once".

// Uptime reports the fraction of checks inside window that saw the tunnel up.
//
// checks is how many readings the answer rests on — over a month at one every
// five minutes, a healthy tunnel has about 8,640. ok is false when there are
// none, which means not measured rather than down.
func (h *History) Uptime(now time.Time, window time.Duration) (pct float64, checks int, ok bool) {
	if h == nil {
		return 0, 0, false
	}
	cutoff := now.Add(-window).Unix()

	var up, total int
	for _, b := range h.Hourly {
		if b.T < cutoff || b.N == 0 {
			continue
		}
		up += b.UpN
		total += b.N
	}
	// The recent five-minute samples cover the window the hourly buckets have
	// not been rolled up into yet — the last hour or so. Without them a tunnel
	// that has just gone down looks perfect until the hour turns over.
	for _, s := range h.Recent {
		if s.T < cutoff || s.T < lastHourStart(h) {
			continue
		}
		total++
		if s.Up {
			up++
		}
	}

	if total == 0 {
		return 0, 0, false
	}
	return float64(up) * 100 / float64(total), total, true
}

// lastHourStart is the start of the hour after the newest rolled-up bucket, so
// the recent samples are counted exactly once: those older than it are already
// inside an Hour.
func lastHourStart(h *History) int64 {
	if len(h.Hourly) == 0 {
		return 0
	}
	newest := h.Hourly[len(h.Hourly)-1].T
	for _, b := range h.Hourly {
		if b.T > newest {
			newest = b.T
		}
	}
	return newest + 3600
}

// UptimeOf is the same answer for a named tunnel, read from the store.
func UptimeOf(name string, window time.Duration) (pct float64, checks int, ok bool) {
	f := Load()
	h, found := f.Tunnels[name]
	if !found {
		return 0, 0, false
	}
	return h.Uptime(time.Now(), window)
}
