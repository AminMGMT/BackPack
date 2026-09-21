package node

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fleet is a fleet of maps. The whole point of injecting Upgrade and Verify is
// that a rollout — code whose failure mode is a fleet-wide outage — can be
// tested without one.
type fleet struct {
	mu sync.Mutex
	// order is every node Upgrade was called on, in the order it happened.
	order []string
	// breaks names nodes that fail verification after upgrading.
	breaks map[string]bool
	// refuses names nodes whose upgrade itself fails.
	refuses map[string]bool
	soaks   []time.Duration
}

func newFleet() *fleet {
	return &fleet{breaks: map[string]bool{}, refuses: map[string]bool{}}
}

func (f *fleet) rollout() *Rollout {
	return &Rollout{
		Upgrade: func(name string) error {
			f.mu.Lock()
			f.order = append(f.order, name)
			refused := f.refuses[name]
			f.mu.Unlock()
			if refused {
				return errors.New("the installer failed")
			}
			return nil
		},
		Verify: func(name string) error {
			f.mu.Lock()
			broken := f.breaks[name]
			f.mu.Unlock()
			if broken {
				return errors.New("its tunnels did not come back")
			}
			return nil
		},
		// No real waiting: the soak is recorded so the test can assert it
		// happened, without the test taking three minutes.
		sleep: func(ctx context.Context, d time.Duration) error {
			f.mu.Lock()
			f.soaks = append(f.soaks, d)
			f.mu.Unlock()
			return ctx.Err()
		},
	}
}

func (f *fleet) touched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.order...)
}

func names(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = string(rune('a' + i))
	}
	return out
}

func TestPlanPutsOneServerFirst(t *testing.T) {
	p := PlanRollout([]string{"c", "a", "b", "d", "e", "f"}, 2, nil)
	if p.Canary != "a" {
		t.Fatalf("canary = %q, want the first in sorted order", p.Canary)
	}
	if len(p.Waves) != 3 {
		t.Fatalf("waves = %v, want five servers in waves of two", p.Waves)
	}
	if p.Total() != 6 {
		t.Fatalf("total = %d, want 6", p.Total())
	}
}

// The plan must be the same every time it is computed, or an operator cannot
// reason about what they are about to read.
func TestPlanIsDeterministic(t *testing.T) {
	first := PlanRollout([]string{"z", "m", "a"}, 2, nil)
	second := PlanRollout([]string{"a", "z", "m"}, 2, nil)
	if first.Canary != second.Canary {
		t.Fatalf("canary differs between runs: %q vs %q", first.Canary, second.Canary)
	}
}

func TestPinnedServersAreLeftOutOfThePlan(t *testing.T) {
	pinned := map[string]Skip{
		"b": {Version: "v1.8.0", Reason: "customer is mid-migration"},
	}
	p := PlanRollout([]string{"a", "b", "c"}, 4, func(n string) (Skip, bool) {
		s, ok := pinned[n]
		return s, ok
	})
	if p.Total() != 2 {
		t.Fatalf("plan touches %d servers, want 2", p.Total())
	}
	if len(p.Skipped) != 1 || p.Skipped[0].Name != "b" {
		t.Fatalf("skipped = %+v", p.Skipped)
	}
	if !strings.Contains(p.Describe(time.Minute), "mid-migration") {
		t.Fatalf("the plan does not say why b is held back:\n%s", p.Describe(time.Minute))
	}
}

// The whole reason for staging: a bad release costs one server, not the fleet.
func TestABadCanaryStopsEverything(t *testing.T) {
	f := newFleet()
	f.breaks["a"] = true
	plan := PlanRollout(names(10), 4, nil)

	res := f.rollout().Run(context.Background(), plan)

	if touched := f.touched(); len(touched) != 1 || touched[0] != "a" {
		t.Fatalf("a bad canary touched %v; the whole point is that it touches one", touched)
	}
	if res.OK() {
		t.Fatal("a rollout whose canary failed reported success")
	}
	if !strings.Contains(res.Halted, "canary") {
		t.Fatalf("halt reason = %q, want it to name the canary", res.Halted)
	}
	if len(res.Untouched) != 9 {
		t.Fatalf("untouched = %v, want the other nine", res.Untouched)
	}
}

// The canary has to be given time before it is judged, or the check happens
// while the service is still starting and passes for the wrong reason.
func TestTheCanaryIsSoakedBeforeItIsJudged(t *testing.T) {
	f := newFleet()
	r := f.rollout()
	r.Soak = 90 * time.Second
	r.Run(context.Background(), PlanRollout(names(3), 4, nil))

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.soaks) != 1 {
		t.Fatalf("soaked %d times, want once — only the canary is soaked", len(f.soaks))
	}
	if f.soaks[0] != 90*time.Second {
		t.Fatalf("soak = %v, want the configured window", f.soaks[0])
	}
}

func TestAGoodRolloutReachesEveryServerInOrder(t *testing.T) {
	f := newFleet()
	plan := PlanRollout(names(9), 4, nil)
	res := f.rollout().Run(context.Background(), plan)

	if !res.OK() {
		t.Fatalf("a healthy fleet halted: %+v", res)
	}
	if len(res.Upgraded) != 9 {
		t.Fatalf("upgraded %d of 9", len(res.Upgraded))
	}
	touched := f.touched()
	if touched[0] != "a" {
		t.Fatalf("the canary was not first: %v", touched)
	}
}

// A failure after the canary passed means the fault is not in the canary's
// environment. Continuing would be choosing to find out how far it goes.
func TestAFailureInAWaveHaltsTheRest(t *testing.T) {
	f := newFleet()
	f.breaks["d"] = true // in the first wave after the canary
	plan := PlanRollout(names(12), 3, nil)

	res := f.rollout().Run(context.Background(), plan)

	if res.Halted == "" {
		t.Fatal("a failure inside a wave did not halt the rollout")
	}
	if len(res.Untouched) == 0 {
		t.Fatal("the halt left nothing untouched, so it did not actually stop")
	}
	// The wave it failed in still finished — the servers were already being
	// upgraded — but nothing after it started.
	if len(f.touched()) >= 12 {
		t.Fatalf("the rollout reached every server despite halting: %v", f.touched())
	}
	for _, want := range []string{"d", "tolerance"} {
		if !strings.Contains(res.Halted, want) {
			t.Fatalf("halt reason %q does not mention %q", res.Halted, want)
		}
	}
}

// A tolerance above zero lets a rollout past a known-bad machine.
func TestToleranceLetsARolloutContinue(t *testing.T) {
	f := newFleet()
	f.breaks["d"] = true
	r := f.rollout()
	r.Tolerance = 1

	res := r.Run(context.Background(), PlanRollout(names(6), 3, nil))

	if res.Halted != "" {
		t.Fatalf("halted despite the tolerance: %s", res.Halted)
	}
	if len(res.Failed) != 1 || res.Failed[0] != "d" {
		t.Fatalf("failed = %v, want just d", res.Failed)
	}
	if res.OK() {
		t.Fatal("a rollout with a failed server reported OK")
	}
	if len(res.Upgraded) != 5 {
		t.Fatalf("upgraded %d, want the other five", len(res.Upgraded))
	}
}

// An upgrade that fails outright is the same halt as one that fails
// verification — from the fleet's point of view the machine is not on the new
// version either way.
func TestAnInstallerFailureIsTreatedAsAFailure(t *testing.T) {
	f := newFleet()
	f.refuses["a"] = true
	res := f.rollout().Run(context.Background(), PlanRollout(names(5), 2, nil))
	if res.Halted == "" {
		t.Fatal("a canary whose installer failed did not halt the rollout")
	}
}

// Without a way to check, staging proves nothing — it is just a slower parallel
// upgrade. Refusing is the honest behaviour.
func TestARolloutThatCannotVerifyRefusesToRun(t *testing.T) {
	f := newFleet()
	r := f.rollout()
	r.Verify = nil
	res := r.Run(context.Background(), PlanRollout(names(5), 2, nil))
	if len(f.touched()) != 0 {
		t.Fatalf("it upgraded %v with no way to check them", f.touched())
	}
	if res.Halted == "" {
		t.Fatal("it ran without a health check")
	}
}

// Cancelling stops between stages, not mid-upgrade: interrupting an upgrade is
// how a machine ends up on neither version.
func TestCancellingStopsBetweenWaves(t *testing.T) {
	f := newFleet()
	ctx, cancel := context.WithCancel(context.Background())
	r := f.rollout()
	var upgraded int
	inner := r.Upgrade
	r.Upgrade = func(name string) error {
		upgraded++
		if upgraded == 1 {
			cancel() // cancel while the canary is going through
		}
		return inner(name)
	}
	res := r.Run(ctx, PlanRollout(names(8), 2, nil))

	// The canary's own upgrade completed rather than being abandoned.
	if len(f.touched()) != 1 {
		t.Fatalf("touched %v, want the cancellation to land between stages", f.touched())
	}
	if res.Halted == "" {
		t.Fatal("a cancelled rollout did not report halting")
	}
}

func TestAnEmptyFleetPlansNothing(t *testing.T) {
	p := PlanRollout(nil, 4, nil)
	if p.Total() != 0 {
		t.Fatalf("total = %d for an empty fleet", p.Total())
	}
	if !strings.Contains(p.Describe(time.Minute), "Nothing to upgrade") {
		t.Fatalf("describe = %q", p.Describe(time.Minute))
	}
}

// A fleet of one still gets a canary — it is simply the only server, and the
// soak and the check still apply.
func TestAFleetOfOneIsAllCanary(t *testing.T) {
	p := PlanRollout([]string{"only"}, 4, nil)
	if p.Canary != "only" || len(p.Waves) != 0 {
		t.Fatalf("plan = %+v", p)
	}
}

// A pin with no reason becomes permanent by default: nobody who finds it later
// knows whether it still applies, so nobody removes it.
func TestPinningRequiresAReason(t *testing.T) {
	if err := Pin("whatever", "   "); err == nil {
		t.Fatal("a pin with no reason was accepted")
	}
}
