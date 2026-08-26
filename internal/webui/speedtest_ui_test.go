package webui

import (
	"os"
	"strings"
	"testing"
)

// readServerSource reads the route table, so a test can check what a route is
// wrapped in rather than what it is named.
func readServerSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("reading the route table: %v", err)
	}
	return string(b)
}

// The measurement is not free and must not be one click away from nothing.
//
// On a port forwarder the receiver on the far server has to bind the port the
// real backend normally holds, so that service is down for as long as the
// measurement runs. The panel has to say which service, and say it before there
// is anything to press — the CLI asks the same question out loud, and a button
// that hid it would be a worse version of the same screen.
func TestTheSpeedTestSaysWhatItCosts(t *testing.T) {
	page := string(dashboardHTML)

	if !strings.Contains(page, "function openSpeedTest(") {
		t.Fatal("there is no speed test in the panel")
	}
	body := page[strings.Index(page, "function renderSpeedPlan("):]
	if end := strings.Index(body, "\nfunction "); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "that backend is down while this runs") {
		t.Error("the plan does not warn that the far end's backend stops for the " +
			"measurement, which is the one thing it costs somebody else")
	}
	if !strings.Contains(body, "Start the receiver on the other server first") {
		t.Error("the plan does not say the far end has to be running the receiver, " +
			"which is the only way the measurement can succeed")
	}
}

// A mapping that cannot carry a measurement is shown with the reason rather
// than hidden — the same choice the CLI makes, so a port that is missing from
// the list is not a mystery.
func TestUnusableMappingsAreShownWithTheirReason(t *testing.T) {
	page := string(dashboardHTML)
	body := page[strings.Index(page, "function renderSpeedPlan("):]
	if end := strings.Index(body, "\nfunction "); end > 0 {
		body = body[:end]
	}
	// The class is assembled rather than written out, so the marker to look for
	// is the branch that produces it.
	if !strings.Contains(body, "'off'") {
		t.Error("unusable mappings are not marked, so they either vanish or look " +
			"pressable")
	}
	if !strings.Contains(body, "(p.targets||[]).forEach") {
		t.Error("only some mappings are rendered; the unusable ones need to be " +
			"listed with their reason, not filtered out")
	}
	if !strings.Contains(body, "esc(t.reason)") {
		t.Error("the reason a mapping cannot be measured is not shown")
	}
}

// The dial has to move while the server measures, or ten seconds of waiting
// reads as a page that has stopped. It is not a live reading — the server
// reports once, at the end — so the sweep must be cleared before the real
// value lands, and both must respect a reduced-motion preference.
func TestTheDialMovesWhileMeasuringAndSettlesAfter(t *testing.T) {
	page := string(dashboardHTML)

	for _, fn := range []string{"function speedSweep(", "function speedShow("} {
		i := strings.Index(page, fn)
		if i < 0 {
			t.Fatalf("%s is missing", fn)
		}
		body := page[i:]
		if end := strings.Index(body, "\nfunction "); end > 0 {
			body = body[:end]
		}
		if !strings.Contains(body, "RING_REDUCE") {
			t.Errorf("%s ignores prefers-reduced-motion", strings.TrimPrefix(fn, "function "))
		}
	}
	run := page[strings.Index(page, "async function runSpeedTest("):]
	if end := strings.Index(run, "\nfunction "); end > 0 {
		run = run[:end]
	}
	if strings.Count(run, "speedSweep(false)") < 2 {
		t.Error("the sweep is not stopped on both the success and the failure path, " +
			"so a failed measurement leaves the dial spinning forever")
	}
}

// Both endpoints change something or reveal where a backend lives, so neither
// belongs to the read-only remote token.
func TestTheSpeedTestEndpointsNeedWriteAuth(t *testing.T) {
	src := readServerSource(t)

	for _, route := range []string{"/api/speedtest/plan", "/api/speedtest"} {
		i := strings.Index(src, `"`+route+`"`)
		if i < 0 {
			t.Fatalf("%s is not registered", route)
		}
		line := src[i:]
		if end := strings.Index(line, "\n"); end > 0 {
			line = line[:end]
		}
		if !strings.Contains(line, "requireAuth") || strings.Contains(line, "requireReadAuth") {
			t.Errorf("%s is not behind requireAuth: %s", route, strings.TrimSpace(line))
		}
	}
}
