package webui

import (
	"context"
	"fmt"
	"strings"

	"github.com/backpack/backpack/internal/node"
)

// The panel's half of a staged fleet update. The sequencing itself lives in
// internal/node/rollout.go, with no knowledge of HTTP; this is the part that
// knows what "healthy" means to this panel and how to say what happened.

// jobRollout is the kind a staged fleet update runs under. One at a time: two
// rollouts over one fleet would each undo the other's canary.
const jobRollout = "rollout"

// rolloutPlan works out what an upgrade-all would do right now.
func (s *server) rolloutPlan() node.Plan {
	list := node.List()
	names := make([]string, 0, len(list))
	pins := make(map[string]node.Skip, len(list))
	for _, n := range list {
		names = append(names, n.Name)
		if skip, ok := n.Pinned(); ok {
			pins[n.Name] = skip
		}
	}
	return node.PlanRollout(names, node.DefaultWave, func(name string) (node.Skip, bool) {
		skip, ok := pins[name]
		return skip, ok
	})
}

// verifyNode decides whether a server came back healthy from its upgrade.
//
// Answering is not enough, and neither is systemd answering. A node is healthy
// when it answers, every enabled tunnel has a process, every one of those
// *holds a peer* by the engine's own account, and none of them is delivering
// into nothing at the far side. Those are four different failures and a rollout
// that checks only the first passes a canary that is broken in the other three.
//
// A node with no tunnels is healthy if it answers; there is nothing else to
// ask, and refusing to proceed past a machine that has nothing to run would
// stop every rollout on the first spare server.
func (s *server) verifyNode(run node.Runner, name string) error {
	if run == nil {
		return fmt.Errorf("managed servers are turned off")
	}
	var info node.Info
	if err := run.Call(name, node.OpHello, nil, &info); err != nil {
		return fmt.Errorf("%s did not answer after the upgrade: %w", name, err)
	}
	_ = node.NoteInfo(name, info)

	var tunnels []node.TunnelState
	if err := run.Call(name, node.OpList, nil, &tunnels); err != nil {
		return fmt.Errorf("%s answered but would not say what it is running: %w", name, err)
	}

	// Three questions, in order of how much they prove.
	//
	// "Is the unit running" is the weakest and it is the one a rollout is most
	// tempted to stop at. A binary that comes back and a tunnel that carries
	// are not the same claim, and the whole reason to upgrade one server first
	// is to find out which one you got.
	var (
		down       []string // systemd has no process
		unpaired   []string // the process is there and the tunnel is not up
		deliverNil []string // the tunnel is up and delivering into nothing
	)
	for _, t := range tunnels {
		if !t.Enabled {
			continue
		}
		if !t.Active {
			down = append(down, t.Name)
			continue
		}
		// The engine's own answer, when it has one. A node still running an
		// older build through an upgrade has no opinion, and absent must not
		// read as "not connected" — that would halt every rollout on its first
		// node until the whole fleet had already been upgraded.
		if t.Connected != nil && !*t.Connected {
			unpaired = append(unpaired, t.Name)
			continue
		}
		// Up, and every connection dying one step past the end of it. Only
		// this node can see it, which is why it crosses the wire at all — and
		// verifying a canary without looking at it was leaving the most useful
		// thing on the table.
		if t.ServiceDown != "" {
			deliverNil = append(deliverNil, t.Name+" ("+t.ServiceDown+")")
		}
	}

	switch {
	case len(down) > 0:
		return fmt.Errorf("%s answered but %d tunnel(s) did not come back: %s",
			name, len(down), strings.Join(down, ", "))
	case len(unpaired) > 0:
		return fmt.Errorf("%s answered and %d tunnel(s) are running without a peer: %s",
			name, len(unpaired), strings.Join(unpaired, ", "))
	case len(deliverNil) > 0:
		return fmt.Errorf("%s answered and %d tunnel(s) are up but delivering into nothing: %s",
			name, len(deliverNil), strings.Join(deliverNil, ", "))
	}
	return nil
}

// describeRollout renders the outcome for the operator.
//
// A halted rollout is reported with what is now on which version, because
// "the rollout stopped" is only half the sentence: the fleet is in a mixed
// state and the next decision depends on knowing exactly which half is which.
func describeRollout(res node.Result) string {
	var b strings.Builder
	if res.OK() {
		fmt.Fprintf(&b, "%d server(s) upgraded.", len(res.Upgraded))
		return b.String()
	}
	if res.Halted != "" {
		fmt.Fprintf(&b, "Rollout halted: %s\n", res.Halted)
	}
	if len(res.Upgraded) > 0 {
		fmt.Fprintf(&b, "On the new version: %s\n", strings.Join(res.Upgraded, ", "))
	}
	if len(res.Failed) > 0 {
		fmt.Fprintf(&b, "Failed (each rolled itself back): %s\n", strings.Join(res.Failed, ", "))
	}
	if len(res.Untouched) > 0 {
		fmt.Fprintf(&b, "Not attempted, still on the old version: %s\n", strings.Join(res.Untouched, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// jobCtx is the lifetime a background job gets.
//
// Deliberately not the request's. A rollout is minutes long and the request
// that started it is answered in milliseconds — tying the job to it would
// cancel the rollout the moment the browser had its reply. It is tied to the
// panel instead, so a panel that shuts down does not leave servers being
// upgraded with nobody watching.
// noteJob records a finished job for the panel's own metrics. Counted here
// rather than read back from control.Jobs' history, which is bounded — a count
// taken from it would fall as old jobs age out, and a counter that goes down is
// one Prometheus reads as a process restart.
func noteJob(err error) { noteJobOutcome(err == nil) }

func (s *server) jobCtx() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}
