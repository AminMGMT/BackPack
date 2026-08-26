package webui

import (
	"strings"
	"testing"
)

// A tunnel's throughput on its card has to move.
//
// Readings arrive six seconds apart, and the chart used to be rebuilt from
// scratch on each one: sp.innerHTML was replaced, so the line jumped from one
// shape to the next and the rate flicked from one number to another. An element
// that is replaced cannot be animated — keeping the svg and its two paths
// across readings is what makes the motion possible at all, so that is the
// property worth guarding.
func TestTheCardChartIsKeptAcrossReadings(t *testing.T) {
	page := string(dashboardHTML)

	if !strings.Contains(page, "function sparkUpdate(") {
		t.Fatal("there is no incremental update for the card chart, so it can only " +
			"be redrawn whole")
	}
	if !strings.Contains(page, "sp.dataset.built") {
		t.Error("nothing records that the chart has already been built, so every " +
			"reading rebuilds it and nothing can be animated")
	}
	// The live card must not go back to replacing the element.
	body := page[strings.Index(page, "function sparkUpdate("):]
	if end := strings.Index(body, "\nfunction "); end > 0 {
		body = body[:end]
	}
	if strings.Contains(body, "sp.innerHTML=sparkSVG") {
		t.Error("sparkUpdate replaces the chart wholesale, which is the jump it " +
			"exists to remove")
	}
}

// The rate reads as a rate: it walks to its new value rather than flicking.
func TestTheRateCountsToItsNewValue(t *testing.T) {
	page := string(dashboardHTML)

	if !strings.Contains(page, "function countBytes(") {
		t.Fatal("the throughput numbers are written straight in, so they flick " +
			"between readings instead of moving")
	}
	body := page[strings.Index(page, "function countBytes("):]
	if end := strings.Index(body, "\nfunction "); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "requestAnimationFrame") {
		t.Error("countBytes does not animate")
	}
	// Formatting every frame is what lets the unit change on the way through.
	if !strings.Contains(body, "humanBytes(from+(to-from)") {
		t.Error("countBytes interpolates the formatted string rather than the " +
			"value, so a KB-to-MB crossing would jump")
	}
}

// Motion is a preference. Someone who has asked for less must still get the
// number and the shape — they just arrive rather than travel.
func TestTheChartHonoursReducedMotion(t *testing.T) {
	page := string(dashboardHTML)

	for _, fn := range []string{"function sparkUpdate(", "function countBytes("} {
		body := page[strings.Index(page, fn):]
		if end := strings.Index(body, "\nfunction "); end > 0 {
			body = body[:end]
		}
		if !strings.Contains(body, "RING_REDUCE") {
			t.Errorf("%s ignores prefers-reduced-motion",
				strings.TrimSuffix(strings.TrimPrefix(fn, "function "), "("))
		}
	}
}

// The places that show a history rather than a live rate — the Details sheet,
// the 24-hour view — are opened, read and closed. They have nothing to travel
// from, and must keep working off the same geometry rather than a second copy
// of it.
func TestTheStaticChartStillExistsAndSharesTheGeometry(t *testing.T) {
	page := string(dashboardHTML)

	if !strings.Contains(page, "function sparkSVG(") {
		t.Fatal("the one-shot chart is gone; the Details sheet and the 24-hour " +
			"view have nothing to render with")
	}
	body := page[strings.Index(page, "function sparkSVG("):]
	if end := strings.Index(body, "\nfunction "); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "sparkSeries(") {
		t.Error("sparkSVG computes its own geometry instead of sharing sparkSeries, " +
			"so the live and static charts can drift apart")
	}
}
