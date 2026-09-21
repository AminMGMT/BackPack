package transport

import "testing"

// When the pool is allowed to grow, and when it is not.
//
// Growth used to be unbounded: a burst of requests could keep adding
// connections with nothing to stop it. The cap and the throughput trigger are
// the two things that decide, and both are arithmetic that is easy to get
// subtly wrong and impossible to notice — a pool that grows too eagerly looks
// like a tunnel that is merely busy, and one that never grows looks like a
// tunnel that is merely slow.

func TestThePoolOnlyGrowsWhenEachConnectionIsWorkingHard(t *testing.T) {
	var p poolLoad
	const configured = 8

	for _, tc := range []struct {
		name                       string
		mbps, live, size, confSize int
		want                       bool
	}{
		{"idle tunnel", 0, 8, 8, configured, false},
		{"no live connections yet", 400, 0, 8, configured, false},
		{"busy overall but spread thin", 8 * (poolScaleMbpsPerConn - 1), 8, 8, configured, false},
		{"each connection at the threshold", 8 * poolScaleMbpsPerConn, 8, 8, configured, true},
		{"each connection well past it", 8 * poolScaleMbpsPerConn * 3, 8, 8, configured, true},
		{"busy, but already at the cap", 4000, 8, configured * poolGrowthLimit, configured, false},
		{"busy, one short of the cap", 4000, 8, configured*poolGrowthLimit - 1, configured, true},
		{"no configured size to scale from", 4000, 8, 8, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.wantsMore(tc.mbps, tc.live, tc.size, tc.confSize); got != tc.want {
				t.Errorf("wantsMore(%d mbps, %d live, size %d, configured %d) = %v, want %v",
					tc.mbps, tc.live, tc.size, tc.confSize, got, tc.want)
			}
		})
	}
}

// The cap is a multiple of what the operator configured, not an absolute — a
// tunnel set up with a pool of 2 and one set up with 64 should be bounded by
// the same reasoning, not the same number.
func TestTheGrowthCapIsRelativeToWhatWasConfigured(t *testing.T) {
	for _, configured := range []int{1, 2, 8, 64} {
		limit := configured * poolGrowthLimit
		if !poolCanGrow(limit-1, configured) {
			t.Errorf("a pool of %d with %d configured was refused growth below its cap",
				limit-1, configured)
		}
		if poolCanGrow(limit, configured) {
			t.Errorf("a pool of %d with %d configured grew past its cap", limit, configured)
		}
	}
	// Zero configured means nothing to scale from, which is not an invitation
	// to grow without limit.
	if poolCanGrow(0, 0) || poolCanGrow(1000, 0) {
		t.Error("a pool with no configured size was allowed to grow")
	}
}

// The first reading has nothing to subtract from, and a counter that went
// backwards means the metrics file was restored from a backup. Both are "no
// opinion" rather than "idle" — reporting a number there would have the pool
// growing or shrinking on an artefact.
func TestThroughputHasNoOpinionWithoutAPreviousReading(t *testing.T) {
	var p poolLoad
	if got := p.mbps(); got != 0 {
		t.Errorf("the first reading returned %d; there is nothing to compare against yet", got)
	}
	// A second immediate call has a previous reading but no elapsed time.
	if got := p.mbps(); got != 0 {
		t.Errorf("a reading taken in the same instant returned %d", got)
	}
}
