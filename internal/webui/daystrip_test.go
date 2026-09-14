package webui

import (
	"io/fs"
	"strings"
	"testing"
)

// The report this exists for: "the daily traffic chart still shows the same
// week five days after installing, while the total above it keeps rising."
//
// The strip was fetched once per page load and then never again — the guard
// deciding whether to fetch tested the fetched value itself, so the first
// success stopped every later attempt for as long as the tab lived. The panel
// is a PWA; tabs live for days. The total above it kept moving the whole time
// because that figure is recomputed from the metrics files on every poll and
// never went through this path at all, which is what made the two disagree and
// sent everybody looking at the backend, where nothing was wrong.
func TestTheDayStripIsRefetched(t *testing.T) {
	loadPanel()

	b, err := fs.ReadFile(panelRoot, "js/views/overview.js")
	if err != nil {
		t.Fatalf("cannot read overview.js: %v", err)
	}
	src := string(b)

	// The exact shape of the bug: having the data is itself the reason not to
	// fetch it again.
	if strings.Contains(src, "asked || dayTotals ||") {
		t.Error("the day strip is fetched only while dayTotals is unset, so the first " +
			"success stops every later fetch and the strip freezes for the life of the tab")
	}

	// Whatever the guard looks like, there has to be something that expires.
	if !strings.Contains(src, "DAY_TOTALS_TTL") {
		t.Error("the day strip has no refetch interval, so it can only ever be read once")
	}
	if !strings.Contains(src, "dayTotalsAt") {
		t.Error("nothing records when the day strip was last fetched, so no interval " +
			"can be enforced")
	}

	// A fetch that failed must not leave the guard held, or one bad round stops
	// the strip permanently — the same failure in a different place.
	if !strings.Contains(src, "finally { asked = false") {
		t.Error("the in-flight guard is not released in a finally, so a failed fetch " +
			"blocks every later one")
	}

	// And the redraw has to notice new data. The region helper replaces a block
	// only when its signature changes, so the strip's signature must be built
	// from the totals themselves.
	if !strings.Contains(src, "region('ovDays', JSON.stringify(dayTotals)") {
		t.Error("the day strip's repaint signature is no longer derived from the " +
			"totals, so refetched data would not be drawn")
	}
}

// Location, ISP and IPv4 are all derived from one address, and the panel marks
// them when that address was inferred rather than held. The marks have to be in
// the repaint signature or a server whose address stops being inferred keeps
// wearing them.
func TestTheAddressSourceIsInTheFactsSignature(t *testing.T) {
	loadPanel()

	b, err := fs.ReadFile(panelRoot, "js/views/overview.js")
	if err != nil {
		t.Fatalf("cannot read overview.js: %v", err)
	}
	src := string(b)

	if !strings.Contains(src, "s.ipv4Source") {
		t.Fatal("overview no longer reads ipv4Source; the panel cannot say whether the " +
			"address it shows is one this machine holds or one inferred from outside")
	}

	sig := src[strings.Index(src, "region('ovFacts'"):]
	sig = sig[:strings.Index(sig, "facts(s))")]
	if !strings.Contains(sig, "s.ipv4Source") {
		t.Error("ipv4Source is drawn but is not in the ovFacts signature, so the " +
			"'(inferred)' marks persist after the address becomes known")
	}
}
