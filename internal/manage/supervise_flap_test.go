package manage

import (
	"testing"
	"time"
)

// Several tunnels restarting at once is a different condition from one tunnel
// restarting several times, and it wants a different answer.
//
// The per-tunnel message sends an operator to look at a path — an MSS clamp, a
// lossy link. That is the right advice for one tunnel and the wrong advice for
// five, where the cause is almost always the machine and looking at any one of
// them wastes the time before somebody checks the clock or the network.

func TestOneFlappingTunnelIsNotAFleetCondition(t *testing.T) {
	now := time.Now()
	restarts := map[string][]time.Time{
		"a": {now.Add(-time.Minute), now.Add(-2 * time.Minute), now.Add(-3 * time.Minute)},
	}
	seen := map[string]time.Time{}
	reportFleetFlap(restarts, seen, now)
	if _, reported := seen[""]; reported {
		t.Error("one tunnel restarting repeatedly was reported as a fleet condition")
	}
}

func TestTwoFlappingTunnelsAreStillACoincidence(t *testing.T) {
	now := time.Now()
	restarts := map[string][]time.Time{
		"a": {now.Add(-time.Minute), now.Add(-2 * time.Minute)},
		"b": {now.Add(-time.Minute), now.Add(-2 * time.Minute)},
	}
	seen := map[string]time.Time{}
	reportFleetFlap(restarts, seen, now)
	if _, reported := seen[""]; reported {
		t.Error("two tunnels were reported as a pattern; two is a coincidence")
	}
}

func TestThreeFlappingTunnelsAreReportedAsOneCondition(t *testing.T) {
	now := time.Now()
	restarts := map[string][]time.Time{
		"a": {now.Add(-time.Minute), now.Add(-2 * time.Minute)},
		"b": {now.Add(-time.Minute), now.Add(-2 * time.Minute)},
		"c": {now.Add(-time.Minute), now.Add(-2 * time.Minute)},
	}
	seen := map[string]time.Time{}
	reportFleetFlap(restarts, seen, now)
	if _, reported := seen[""]; !reported {
		t.Fatal("three tunnels restarting together was not reported")
	}

	// And only once a window, or the condition buries everything else while it
	// lasts.
	before := seen[""]
	reportFleetFlap(restarts, seen, now.Add(time.Minute))
	if !seen[""].Equal(before) {
		t.Error("the fleet condition was reported twice inside one window")
	}
	// And again once the window has passed — with restarts that are still
	// recent at that later moment, because a condition that has stopped
	// happening is one that should stop being reported. (The first version of
	// this test reused the original restarts and asserted the opposite, which
	// was asking for a report about a machine that was fine again.)
	later := now.Add(flapWindow + time.Minute)
	fresh := map[string][]time.Time{
		"a": {later.Add(-time.Minute), later.Add(-2 * time.Minute)},
		"b": {later.Add(-time.Minute), later.Add(-2 * time.Minute)},
		"c": {later.Add(-time.Minute), later.Add(-2 * time.Minute)},
	}
	reportFleetFlap(fresh, seen, later)
	if seen[""].Equal(before) {
		t.Error("a condition that was still happening was never reported again")
	}
}

// Restarts that have aged out do not count, or a machine that had a bad hour
// last week is reported as flapping now.
func TestStaleRestartsDoNotMakeAFleetCondition(t *testing.T) {
	now := time.Now()
	old := now.Add(-flapWindow - time.Hour)
	restarts := map[string][]time.Time{
		"a": {old, old}, "b": {old, old}, "c": {old, old},
	}
	seen := map[string]time.Time{}
	reportFleetFlap(restarts, seen, now)
	if _, reported := seen[""]; reported {
		t.Error("restarts from outside the window were counted")
	}
}

// countWithinWindow must not do what recentRestarts does. recentRestarts
// appends now, because every caller has just restarted something; counting with
// it would report one restart more than happened, and at a threshold of two
// that means reporting a tunnel that restarted once.
func TestCountingDoesNotInventARestart(t *testing.T) {
	now := time.Now()
	one := []time.Time{now.Add(-time.Minute)}
	if got := countWithinWindow(one, now); got != 1 {
		t.Fatalf("counted %d restarts from a list of one", got)
	}
	if got := len(recentRestarts(one, now)); got != 2 {
		t.Fatalf("recentRestarts returned %d; it is supposed to add the one that just "+
			"happened, and this test exists to keep the two helpers distinct", got)
	}
}
