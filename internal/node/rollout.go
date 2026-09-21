package node

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Staged fleet updates.
//
// "Upgrade all" replaced the binary on every managed server at once, in
// parallel, and reported which ones failed. That is the right shape for a fleet
// of one and an act of faith for a fleet of twenty: a release with a fault in it
// takes the whole fleet down before anybody has read the first error, and the
// per-node rollback that already exists cannot help, because by then every node
// has rolled back into a different state and nobody knows which.
//
// The expensive half of this problem was already solved. Each node verifies its
// own upgrade and rolls itself back when it fails; that is built, proven, and
// untouched here. What was missing is order.
//
// A rollout therefore does three things and no more:
//
//   - one canary first, held for a soak window and then verified, so a bad
//     release costs one server rather than all of them;
//   - the rest in waves, each verified before the next begins, halting when a
//     wave fails more than the threshold allows;
//   - a plan, computed before anything is touched, so the operator can read
//     what is about to happen rather than find out.
//
// It also honours a per-node pin, so "why is that one still behind" has an
// answer written down next to it instead of living in somebody's memory.

// Defaults. They are conservative on purpose: the cost of a rollout that takes
// an extra ten minutes is nothing next to the cost of one that does not stop.
const (
	// DefaultSoak is how long the canary runs before it is judged. Long enough
	// for a tunnel to re-establish and carry something, short enough that an
	// operator will actually wait for it.
	DefaultSoak = 3 * time.Minute

	// DefaultWave is how many servers move at once after the canary.
	DefaultWave = 4

	// DefaultTolerance is how many failures a wave may contain before the
	// rollout stops. Zero: a second failure after the canary passed means the
	// fault is not in the canary's environment, and continuing would be
	// choosing to find out how far it goes.
	DefaultTolerance = 0
)

// Skip is a node the plan will not touch, and why.
type Skip struct {
	Name    string
	Version string // what it is pinned to
	Reason  string
}

// Plan is what a rollout will do, worked out before anything changes.
//
// It is a value with no behaviour: an operator reads it, a test asserts on it,
// and Run executes it. Computing it separately is the point — a rollout whose
// shape can only be discovered by starting it is the thing being fixed.
type Plan struct {
	Canary  string
	Waves   [][]string
	Skipped []Skip
}

// Total is how many servers the plan will touch.
func (p Plan) Total() int {
	n := 0
	if p.Canary != "" {
		n = 1
	}
	for _, w := range p.Waves {
		n += len(w)
	}
	return n
}

// Describe renders the plan as the operator will read it.
func (p Plan) Describe(soak time.Duration) string {
	var b strings.Builder
	if p.Canary == "" && len(p.Waves) == 0 {
		b.WriteString("Nothing to upgrade.")
		if len(p.Skipped) > 0 {
			fmt.Fprintf(&b, " %d server(s) are pinned.", len(p.Skipped))
		}
		return b.String()
	}
	fmt.Fprintf(&b, "%d server(s) will be upgraded.\n", p.Total())
	if p.Canary != "" {
		fmt.Fprintf(&b, "  1. %s alone, then %s of soak and a health check.\n", p.Canary, soak)
	}
	for i, w := range p.Waves {
		fmt.Fprintf(&b, "  %d. %s\n", i+2, strings.Join(w, ", "))
	}
	for _, s := range p.Skipped {
		fmt.Fprintf(&b, "  skipped: %s is pinned to %s", s.Name, s.Version)
		if s.Reason != "" {
			fmt.Fprintf(&b, " (%s)", s.Reason)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// PlanRollout works out the order. names is the fleet; pinned reports a node's
// pin, if it has one.
//
// The canary is chosen as the *first name in sorted order*, deliberately rather
// than at random: a rollout that picks a different first server each time cannot
// be reasoned about, and an operator who wants a particular canary can get one
// by naming it so it sorts first. Predictability is worth more here than
// spreading the risk, because the risk is the same on every node.
func PlanRollout(names []string, wave int, pinned func(string) (Skip, bool)) Plan {
	if wave < 1 {
		wave = DefaultWave
	}
	var eligible []string
	var plan Plan
	for _, n := range names {
		if pinned != nil {
			if s, ok := pinned(n); ok {
				s.Name = n
				plan.Skipped = append(plan.Skipped, s)
				continue
			}
		}
		eligible = append(eligible, n)
	}
	sort.Strings(eligible)
	sort.Slice(plan.Skipped, func(i, j int) bool { return plan.Skipped[i].Name < plan.Skipped[j].Name })
	if len(eligible) == 0 {
		return plan
	}
	plan.Canary = eligible[0]
	for rest := eligible[1:]; len(rest) > 0; {
		n := min(wave, len(rest))
		plan.Waves = append(plan.Waves, rest[:n])
		rest = rest[n:]
	}
	return plan
}

// Event is one thing that happened, for the log an operator watches.
type Event struct {
	Stage   string // "canary", "wave 2", "halt", "done"
	Node    string // empty for stage-level events
	Message string
	Err     error
}

// Rollout executes a Plan.
//
// Upgrade and Verify are injected rather than reached for, which is what makes
// the whole sequencing testable against a fleet of maps instead of a fleet of
// servers — and this is code whose failure mode is a fleet-wide outage, so
// being able to test it without one is not a nicety.
type Rollout struct {
	// Upgrade replaces the binary on one server and returns when it is done.
	Upgrade func(name string) error

	// Verify reports whether a server is healthy after its upgrade. A nil
	// Verify means the rollout cannot tell, and it then refuses to run rather
	// than staging blindly — staged updates whose stages prove nothing are
	// slower than upgrading everything at once and no safer.
	Verify func(name string) error

	// Soak is how long the canary is left alone before Verify is asked. Zero
	// uses DefaultSoak.
	Soak time.Duration

	// Tolerance is how many failures one wave may contain before halting.
	Tolerance int

	// OnEvent receives the running commentary. Optional.
	OnEvent func(Event)

	// sleep is time.Sleep, replaced in tests so a soak window costs no time.
	sleep func(context.Context, time.Duration) error
}

// Result is what happened.
type Result struct {
	Upgraded []string
	Failed   []string
	// Halted is set when the rollout stopped early, with the reason.
	Halted string
	// Untouched are the servers the halt left on the old version. They are
	// listed because "the rollout stopped" is only half the sentence an
	// operator needs; the other half is which machines are now in which state.
	Untouched []string
}

// OK reports whether the whole plan ran without halting or failures.
func (r Result) OK() bool { return r.Halted == "" && len(r.Failed) == 0 }

func (r *Rollout) emit(e Event) {
	if r.OnEvent != nil {
		r.OnEvent(e)
	}
}

func (r *Rollout) wait(ctx context.Context, d time.Duration) error {
	if r.sleep != nil {
		return r.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run executes the plan. It returns when the plan is finished, halted, or the
// context is done — and a cancelled rollout halts between stages rather than
// mid-upgrade, because interrupting an upgrade is how a node ends up in neither
// version.
func (r *Rollout) Run(ctx context.Context, plan Plan) Result {
	var res Result
	remaining := func(fromWave int) []string {
		var out []string
		for i := fromWave; i < len(plan.Waves); i++ {
			out = append(out, plan.Waves[i]...)
		}
		return out
	}

	if r.Verify == nil {
		res.Halted = "this rollout cannot check whether a server came back, so it will not stage one"
		res.Untouched = append([]string{plan.Canary}, remaining(0)...)
		r.emit(Event{Stage: "halt", Message: res.Halted})
		return res
	}

	soak := r.Soak
	if soak <= 0 {
		soak = DefaultSoak
	}

	// The canary.
	if plan.Canary != "" {
		r.emit(Event{Stage: "canary", Node: plan.Canary, Message: "upgrading"})
		if err := r.upgradeOne(ctx, plan.Canary, soak); err != nil {
			res.Failed = append(res.Failed, plan.Canary)
			res.Halted = fmt.Sprintf("the canary %s did not come back: %v", plan.Canary, err)
			res.Untouched = remaining(0)
			r.emit(Event{Stage: "halt", Node: plan.Canary, Message: res.Halted, Err: err})
			return res
		}
		res.Upgraded = append(res.Upgraded, plan.Canary)
		r.emit(Event{Stage: "canary", Node: plan.Canary, Message: "healthy"})
	}

	for i, wave := range plan.Waves {
		stage := fmt.Sprintf("wave %d", i+2)
		if err := ctx.Err(); err != nil {
			res.Halted = "the rollout was stopped"
			res.Untouched = remaining(i)
			r.emit(Event{Stage: "halt", Message: res.Halted})
			return res
		}
		r.emit(Event{Stage: stage, Message: fmt.Sprintf("upgrading %d server(s)", len(wave))})

		var failed []string
		for _, name := range wave {
			// No soak inside a wave: the canary already bought the confidence
			// that the release works at all, and a soak per wave would make a
			// twenty-server rollout an hour long for no new information.
			if err := r.upgradeOne(ctx, name, 0); err != nil {
				failed = append(failed, name)
				res.Failed = append(res.Failed, name)
				r.emit(Event{Stage: stage, Node: name, Message: "failed", Err: err})
				continue
			}
			res.Upgraded = append(res.Upgraded, name)
			r.emit(Event{Stage: stage, Node: name, Message: "healthy"})
		}

		if len(failed) > r.Tolerance {
			res.Halted = fmt.Sprintf("%s: %d server(s) failed (%s), which is past the tolerance of %d",
				stage, len(failed), strings.Join(failed, ", "), r.Tolerance)
			res.Untouched = remaining(i + 1)
			r.emit(Event{Stage: "halt", Message: res.Halted})
			return res
		}
	}

	r.emit(Event{Stage: "done", Message: fmt.Sprintf("%d server(s) upgraded", len(res.Upgraded))})
	return res
}

// upgradeOne upgrades a server, waits out the soak, and verifies it.
func (r *Rollout) upgradeOne(ctx context.Context, name string, soak time.Duration) error {
	if err := r.Upgrade(name); err != nil {
		return err
	}
	if soak > 0 {
		if err := r.wait(ctx, soak); err != nil {
			return fmt.Errorf("the soak was interrupted: %w", err)
		}
	}
	return r.Verify(name)
}
