package tunhist

import (
	"testing"
	"time"
)

// Uptime, from the samples already being kept.
//
// `Hour` holds UpN of N five-minute checks and the comment on it says that is
// what an honest uptime percentage is made of — and nothing turned it into one.
// "This tunnel was up 99.2% last month" is the number an operator is actually
// asked for, and the data for it has been on disk all along.
//
// The part that has to be right is the distinction between *down* and *not
// measured*. A tunnel created yesterday was not down for the twenty-nine days
// before that, and reporting it as 3% uptime is worse than reporting nothing:
// it is a number somebody will act on.

func hours(now time.Time, n int, upN, of int) []Hour {
	out := make([]Hour, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, Hour{
			T:   now.Add(-time.Duration(i) * time.Hour).Unix(),
			UpN: upN, N: of,
		})
	}
	return out
}

func TestUptimeIsTheFractionOfChecksThatSawItUp(t *testing.T) {
	now := time.Now()
	h := &History{Hourly: hours(now, 24, 12, 12)} // up in every check

	pct, checks, ok := h.Uptime(now, 24*time.Hour)
	if !ok {
		t.Fatal("a full day of samples reported nothing")
	}
	if checks != 24*12 {
		t.Fatalf("counted %d checks, want %d", checks, 24*12)
	}
	if pct != 100 {
		t.Fatalf("uptime = %.2f%%, want 100", pct)
	}
}

func TestUptimeCountsTheChecksThatMissed(t *testing.T) {
	now := time.Now()
	// Up in 9 of every 12 checks: three quarters.
	h := &History{Hourly: hours(now, 24, 9, 12)}

	pct, _, ok := h.Uptime(now, 24*time.Hour)
	if !ok {
		t.Fatal("reported nothing")
	}
	if pct < 74.9 || pct > 75.1 {
		t.Fatalf("uptime = %.2f%%, want 75", pct)
	}
}

// The distinction that makes the number safe to publish.
func TestATunnelWithNoHistoryReportsNothingRatherThanZero(t *testing.T) {
	h := &History{}
	if pct, _, ok := h.Uptime(time.Now(), 30*24*time.Hour); ok {
		t.Fatalf("a tunnel with no samples reported %.2f%% uptime; not measured is "+
			"not the same as down, and a number somebody acts on is worse than none", pct)
	}
}

// A tunnel younger than the window is measured over the part of the window it
// existed for, not over the whole of it.
func TestAYoungTunnelIsMeasuredOverItsOwnLifetime(t *testing.T) {
	now := time.Now()
	// Two hours of perfect uptime, asked about over thirty days.
	h := &History{Hourly: hours(now, 2, 12, 12)}

	pct, checks, ok := h.Uptime(now, 30*24*time.Hour)
	if !ok {
		t.Fatal("reported nothing for a tunnel with two hours of history")
	}
	if pct != 100 {
		t.Fatalf("uptime = %.2f%%; the twenty-nine days before it existed were "+
			"counted as downtime", pct)
	}
	if checks != 24 {
		t.Fatalf("counted %d checks, want the 24 that exist", checks)
	}
}

// Buckets older than the window are not counted, or last month's outage
// follows a tunnel around for ever.
func TestSamplesOutsideTheWindowAreIgnored(t *testing.T) {
	now := time.Now()
	h := &History{Hourly: []Hour{
		{T: now.Add(-48 * time.Hour).Unix(), UpN: 0, N: 12}, // an old outage
		{T: now.Add(-1 * time.Hour).Unix(), UpN: 12, N: 12},
	}}

	pct, checks, ok := h.Uptime(now, 24*time.Hour)
	if !ok {
		t.Fatal("reported nothing")
	}
	if checks != 12 {
		t.Fatalf("counted %d checks, want only the 12 inside the window", checks)
	}
	if pct != 100 {
		t.Fatalf("uptime = %.2f%%; an outage from outside the window was counted", pct)
	}
}

// An hour recorded with no checks in it contributes nothing rather than
// counting as an hour of downtime.
func TestAnEmptyBucketIsNotDowntime(t *testing.T) {
	now := time.Now()
	h := &History{Hourly: []Hour{
		{T: now.Add(-2 * time.Hour).Unix(), UpN: 0, N: 0},
		{T: now.Add(-1 * time.Hour).Unix(), UpN: 12, N: 12},
	}}
	pct, checks, ok := h.Uptime(now, 24*time.Hour)
	if !ok || checks != 12 || pct != 100 {
		t.Fatalf("uptime = %.2f%% over %d checks (ok=%v); an hour with no checks "+
			"in it was treated as an hour of downtime", pct, checks, ok)
	}
}
