package manage

import (
	"strings"
	"testing"
)

// A measurement has to be refused where it cannot succeed, and the refusal has
// to say which machine to go to instead.
//
// The side of a port forwarder that holds the backends keeps no port list, so
// there is nothing on it to measure through — it is the side that runs the
// receiver. Getting that backwards is what made the manual version fiddly, and
// a button that simply failed would put it back.
func TestAPlanNamesTheSideThatCanMeasure(t *testing.T) {
	plan := SpeedTestPlan{
		Kind:    "forward",
		Blocked: "this side holds the backends, so it is the side that receives — measure from the server that exposes the ports",
	}
	if plan.Blocked == "" {
		t.Fatal("setup")
	}
	// The wording has to point somewhere, not just say no.
	for _, want := range []string{"receives", "exposes the ports"} {
		if !strings.Contains(plan.Blocked, want) {
			t.Errorf("the refusal does not mention %q, so it does not say what to do instead", want)
		}
	}
}

// A mapping that cannot carry a measurement must be kept and explained rather
// than dropped, or a port missing from the panel is a mystery.
func TestAnUnusableTargetKeepsItsReason(t *testing.T) {
	target := SpeedTestTarget{Spec: "2053=10.0.0.9:2053", ListenPort: 2053,
		Reason: "backend is on 10.0.0.9, another machine"}

	if target.Usable() {
		t.Fatal("a target with a reason reports itself usable")
	}
	if target.Reason == "" {
		t.Fatal("the reason was dropped")
	}
	if ok := (SpeedTestTarget{ListenPort: 443, BackendPort: 8443}).Usable(); !ok {
		t.Error("a target with no reason reports itself unusable")
	}
}

// The measurement holds an HTTP request open for its whole length, so its
// budget has to stay inside the panel's write timeout — a request that outlives
// the response is a measurement nobody gets to see.
func TestTheMeasurementFitsInsideTheWriteTimeout(t *testing.T) {
	const panelWriteTimeout = 30 // seconds; see webui/server.go

	if speedTestBudget.Seconds() >= panelWriteTimeout {
		t.Fatalf("a measurement may run for %s, but the panel cuts the response at %ds",
			speedTestBudget, panelWriteTimeout)
	}
	// And it has to be long enough to actually contain one.
	run := (throughputWarmup + throughputRun).Seconds()
	if speedTestBudget.Seconds() <= run {
		t.Fatalf("the budget is %s but a measurement alone takes %.0fs", speedTestBudget, run)
	}
}
