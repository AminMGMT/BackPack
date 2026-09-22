package manage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// feed drives n checks with the given per-check deltas and returns the last
// state. It exists because every case below is "hold this shape for a while and
// see what the watchdog concludes".
func feed(w *flowWatch, name string, dIn, dOut uint64, n int, t0 time.Time) flowState {
	var in, out uint64
	var st flowState
	// Prime: the first observation has nothing to compare against.
	st = w.observe(name, in, out, t0)
	for i := 1; i <= n; i++ {
		in += dIn
		out += dOut
		st = w.observe(name, in, out, t0.Add(time.Duration(i)*wdInterval))
	}
	return st
}

// The failure the whole thing exists for: connected, sending, and nothing
// coming back.
func TestOneWayTrafficIsAStall(t *testing.T) {
	w := newFlowWatch()
	got := feed(w, "t", 0, stallProgress, stallChecks, time.Now())
	if got != flowStalled {
		t.Fatalf("state = %v, want stalled", got)
	}
}

// The case that would make this feature worse than nothing: most tunnels are
// idle most of the time, and a watchdog that restarted on silence would restart
// every tunnel on the host every night.
func TestAnIdleTunnelIsNotAStall(t *testing.T) {
	w := newFlowWatch()
	if got := feed(w, "t", 0, 0, stallChecks*3, time.Now()); got != flowIdle {
		t.Fatalf("an idle tunnel reported %v", got)
	}
	if action, _ := w.decide("t", time.Now()); action != stallWatch {
		t.Fatalf("an idle tunnel provoked action %v", action)
	}
}

func TestATunnelCarryingBothWaysIsHealthy(t *testing.T) {
	w := newFlowWatch()
	if got := feed(w, "t", stallProgress, stallProgress, stallChecks*2, time.Now()); got != flowOK {
		t.Fatalf("a working tunnel reported %v", got)
	}
}

// A brief lopsided moment is an ordinary upload, not a fault.
func TestAShortLopsidedBurstIsNotAStall(t *testing.T) {
	w := newFlowWatch()
	if got := feed(w, "t", 0, stallProgress, stallChecks-1, time.Now()); got == flowStalled {
		t.Fatal("a burst shorter than the run length was called a stall")
	}
}

// Trickle in the quiet direction still counts as an answer. Only *nothing*
// coming back is a stall, which is what makes the signal unambiguous.
func TestATrickleBackIsNotAStall(t *testing.T) {
	w := newFlowWatch()
	if got := feed(w, "t", 1, stallProgress, stallChecks*2, time.Now()); got != flowOK {
		t.Fatalf("a tunnel answering at all reported %v", got)
	}
}

// A stalled sender eventually gives up and the tunnel goes quiet. Reading that
// as recovery would be the watchdog looking away at the worst moment.
func TestGoingQuietDoesNotClearAStall(t *testing.T) {
	t0 := time.Now()
	w := newFlowWatch()
	if got := feed(w, "t", 0, stallProgress, stallChecks, t0); got != flowStalled {
		t.Fatal("setup: expected a stall")
	}
	// Same counters from here on: nothing moving at all.
	r := w.seen["t"]
	if got := w.observe("t", r.in, r.out, t0.Add(time.Hour)); got != flowStalled {
		t.Fatalf("a stall that went quiet reported %v", got)
	}
}

// A restarted engine's counters start from zero. Reading that as a huge advance
// in one direction would invent a stall at precisely the wrong moment.
func TestCountersGoingBackwardsDoNotInventAStall(t *testing.T) {
	t0 := time.Now()
	w := newFlowWatch()
	w.observe("t", 10<<20, 10<<20, t0)
	for i := 1; i <= stallChecks+2; i++ {
		// Both counters reset and then climb together, as a healthy restarted
		// tunnel's would.
		st := w.observe("t", uint64(i)*stallProgress, uint64(i)*stallProgress,
			t0.Add(time.Duration(i)*wdInterval))
		if st == flowStalled {
			t.Fatalf("a counter reset was read as a stall at check %d", i)
		}
	}
}

// The ladder: notice it, ask the engine to restart its own transport, restart
// the process once that has not helped, and stop restarting when restarting is
// plainly not the fix.
func TestTheResponseIsGraduated(t *testing.T) {
	t0 := time.Now()
	w := newFlowWatch()
	if got := feed(w, "t", 0, stallProgress, stallChecks, t0); got != flowStalled {
		t.Fatal("setup: expected a stall")
	}
	now := t0.Add(time.Duration(stallChecks) * wdInterval)

	// First rung: say so, do nothing.
	action, msg := w.decide("t", now)
	if action != stallReport {
		t.Fatalf("first action = %v, want a report", action)
	}
	if !strings.Contains(msg, "Watching it") {
		t.Fatalf("the first report did not say it was only watching: %q", msg)
	}

	// Still inside the grace period: still nothing.
	if action, _ := w.decide("t", now.Add(stallRestartAfter/2)); action != stallWatch {
		t.Fatalf("acted inside the grace period: %v", action)
	}

	// Past it: the cheap rung first. A transport restart keeps the process and
	// everything it is holding, and it clears every stall that is about the
	// transport rather than about the path — which is most of them.
	now = now.Add(stallRestartAfter + time.Second)
	action, msg = w.decide("t", now)
	if action != stallReload {
		t.Fatalf("the rung after the report was %v, want a transport restart", action)
	}
	if !strings.Contains(msg, "transport") {
		t.Fatalf("the transport-restart report does not say what it is doing: %q", msg)
	}

	// And it is spent once. Repeating it would only delay the rung that might
	// say something new.
	for i := 1; i <= stallGiveUpAfter; i++ {
		now = now.Add(stallRestartAfter + time.Second)
		action, msg = w.decide("t", now)
		if action != stallRestart {
			t.Fatalf("rung %d = %v, want a restart", i, action)
		}
		if want := fmt.Sprintf("%d/%d", i, stallGiveUpAfter); !strings.Contains(msg, want) {
			t.Errorf("restart %d reported %q, which does not say it is %s", i, msg, want)
		}
	}

	// And then stop, with something an operator can act on.
	now = now.Add(stallRestartAfter + time.Second)
	action, msg = w.decide("t", now)
	if action != stallGiveUp {
		t.Fatalf("after %d restarts the action was %v, want giving up", stallGiveUpAfter, action)
	}
	for _, want := range []string{"MTU", "Diagnose", "not fixing"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the give-up report does not mention %q: %q", want, msg)
		}
	}

	// Having given up, it must go quiet rather than keep reporting.
	if action, _ := w.decide("t", now.Add(time.Minute)); action != stallWatch {
		t.Fatalf("kept acting after giving up: %v", action)
	}
	if action, _ := w.decide("t", now.Add(stallQuietFor+time.Minute)); action == stallWatch {
		t.Fatal("stayed quiet for ever after giving up")
	}
}

// Recovery has to reset the whole ladder, including a give-up — otherwise a
// tunnel that was stalled this morning is unprotected for the rest of the day.
func TestRecoveryResetsTheLadder(t *testing.T) {
	t0 := time.Now()
	w := newFlowWatch()
	feed(w, "t", 0, stallProgress, stallChecks, t0)
	w.decide("t", t0)
	if w.seen["t"].since.IsZero() {
		t.Fatal("setup: expected a live stall")
	}

	// Both directions move again.
	r := w.seen["t"]
	w.observe("t", r.in+stallProgress, r.out+stallProgress, t0.Add(time.Minute))

	r = w.seen["t"]
	if r.runs != 0 || !r.since.IsZero() || r.restarts != 0 || r.reported {
		t.Fatalf("recovery left state behind: %+v", r)
	}
}

// Each tunnel is tracked separately; one stalling must not implicate another.
func TestTunnelsAreTrackedIndependently(t *testing.T) {
	t0 := time.Now()
	w := newFlowWatch()
	feed(w, "sick", 0, stallProgress, stallChecks, t0)
	feed(w, "well", stallProgress, stallProgress, stallChecks, t0)

	if action, _ := w.decide("sick", t0.Add(time.Minute)); action != stallReport {
		t.Fatalf("the stalled tunnel was not reported: %v", action)
	}
	if action, _ := w.decide("well", t0.Add(time.Minute)); action != stallWatch {
		t.Fatalf("a healthy tunnel was acted on: %v", action)
	}
}

// A tunnel stopped on purpose must not carry its history into the next start.
func TestForgetClearsATunnel(t *testing.T) {
	w := newFlowWatch()
	feed(w, "t", 0, stallProgress, stallChecks, time.Now())
	w.forget("t")
	if _, ok := w.seen["t"]; ok {
		t.Fatal("forget left the tunnel behind")
	}
}

// The frozen direction has to be named, because "in" and "out" send an operator
// to two completely different places.
func TestTheReportNamesTheFrozenDirection(t *testing.T) {
	t0 := time.Now()

	out := newFlowWatch()
	feed(out, "t", 0, stallProgress, stallChecks, t0)
	if _, msg := out.decide("t", t0); !strings.Contains(msg, "nothing is coming back") {
		t.Fatalf("outbound stall reported as %q", msg)
	}

	in := newFlowWatch()
	feed(in, "t", stallProgress, 0, stallChecks, t0)
	if _, msg := in.decide("t", t0); !strings.Contains(msg, "nothing is going out") {
		t.Fatalf("inbound stall reported as %q", msg)
	}
}

// A watchdog restart must not forget what it was in the middle of.
//
// flowWatch lives in the watchdog's memory, so restarting the monitor service —
// an update, a crash, an operator — reset every ladder mid-climb. That is
// wrong in both directions: a tunnel two restarts into a stall got its count
// back, and a tunnel the watchdog had deliberately *given up on* started being
// restarted again every few minutes.
//
// The second is the one that matters. Giving up is a decision: the stall
// survived two restarts, so restarting is not the fix, and the quiet period
// exists to stop the churn. Forgetting it turns that decision into a loop.
func TestTheStallLadderSurvivesAWatchdogRestart(t *testing.T) {
	dir := t.TempDir()
	old := flowStatePath
	flowStatePath = filepath.Join(dir, "flow.json")
	t.Cleanup(func() { flowStatePath = old })

	t0 := time.Now()
	w := newFlowWatch()
	feed(w, "t", 0, stallProgress, stallChecks, t0)

	// Climb the whole ladder: report, transport restart, restart, restart,
	// give up.
	now := t0.Add(time.Duration(stallChecks) * wdInterval)
	w.decide("t", now)
	for i := 0; i < stallGiveUpAfter+1; i++ { // +1 for the transport-restart rung
		now = now.Add(stallRestartAfter + time.Second)
		w.decide("t", now)
	}
	now = now.Add(stallRestartAfter + time.Second)
	if action, _ := w.decide("t", now); action != stallGiveUp {
		t.Fatalf("setup: expected the ladder to reach giving up, got %v", action)
	}
	w.save()

	// The watchdog restarts.
	next := newFlowWatch()
	next.load()

	// And it must still be quiet about this tunnel, rather than starting the
	// ladder again from the bottom.
	if action, _ := next.decide("t", now.Add(time.Minute)); action != stallWatch {
		t.Fatalf("after a restart the watchdog wanted to %v a tunnel it had already "+
			"given up on — which turns a decision into a loop", action)
	}
	// And the quiet period still ends when it was going to.
	if action, _ := next.decide("t", now.Add(stallQuietFor+time.Minute)); action == stallWatch {
		t.Fatal("the quiet period outlived the restart and never ended")
	}
}

// A saved file that cannot be read is not a reason to stop watching. The
// watchdog's job is to notice things; starting from nothing is a worse outcome
// than starting from nothing *and* refusing to run.
func TestAnUnreadableStallFileIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	old := flowStatePath
	flowStatePath = filepath.Join(dir, "flow.json")
	t.Cleanup(func() { flowStatePath = old })

	if err := os.WriteFile(flowStatePath, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	w := newFlowWatch()
	w.load() // must not panic
	if action, _ := w.decide("anything", time.Now()); action != stallWatch {
		t.Fatalf("a corrupt file produced action %v", action)
	}
}

// Saving must not leave a half-written file behind, because the next start
// reads it.
func TestTheStallFileIsWrittenWholeOrNotAtAll(t *testing.T) {
	dir := t.TempDir()
	old := flowStatePath
	flowStatePath = filepath.Join(dir, "flow.json")
	t.Cleanup(func() { flowStatePath = old })

	w := newFlowWatch()
	feed(w, "t", 0, stallProgress, stallChecks, time.Now())
	w.decide("t", time.Now())
	w.save()

	info, err := os.Stat(flowStatePath)
	if err != nil {
		t.Fatalf("nothing was written: %v", err)
	}
	// It names tunnels and their state; root's business, nobody else's.
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("mode = %v, want nothing for group or other", mode)
	}
	if _, err := os.Stat(flowStatePath + ".tmp"); err == nil {
		t.Error("a temporary file was left behind")
	}
}
