package webui

import (
	"io/fs"
	"strings"
	"testing"
)

// The report this exists for: "the server cards constantly look like they are
// reloading, and CPU jumps between 0%, 100% and 50%."
//
// Two separate faults wearing one symptom. This file holds the half that lives
// in the page; the reading itself is fixed in sysstat and node.
//
// The fleet page already reconciled its grid — a card whose content had not
// changed was supposed to be left alone — and it was defeated by what it
// compared. The signature was built from the server's whole `info` object and
// from `lastSeen`: the processor moves by a percent between polls and the
// timestamp moves by six seconds, so no signature ever matched, every card was
// replaced four times a minute, and the entrance animation ran again each time.
func TestTheFleetGridIsNotRebuiltOnEveryPoll(t *testing.T) {
	loadPanel()

	b, err := fs.ReadFile(panelRoot, "js/views/servers.js")
	if err != nil {
		t.Fatalf("cannot read servers.js: %v", err)
	}
	src := string(b)

	// The exact shape of the bug: the readings and the timestamp inside the
	// value the grid compares.
	if strings.Contains(src, "d.lastSeen, d.tunnels || [], d.info || {}") {
		t.Error("the card signature is built from the whole info object and lastSeen, " +
			"both of which change on every poll — so every card is rebuilt every poll")
	}

	// Searched inside the signature only. Every one of these names also appears
	// elsewhere in the file and must: the card draws the readings and paintLive
	// writes them. A search over the whole file cannot tell a value being
	// compared from the same value being drawn, and fails on correct code.
	sig := blockOf(t, src, "const sigOf = d => {", "\n  };")
	for _, moving := range []string{"cpuPercent", "memPercent", "memUsed", "uptime", "lastSeen"} {
		if strings.Contains(sig, moving) {
			t.Errorf("%s is in the card signature and it changes on every poll, so no "+
				"signature ever matches and every card is rebuilt four times a minute", moving)
		}
	}

	// And the other half: something has to write them in.
	if !strings.Contains(src, "function paintLive(") {
		t.Fatal("there is no paintLive, so a card that is not rebuilt never updates")
	}
	if !strings.Contains(src, "paintLive(held.el, n)") {
		t.Error("paintLive is never called from the branch where the signature matched, " +
			"so an unchanged card keeps its first reading forever")
	}
	// It has to find the meters again, which is what the hooks are for.
	if !strings.Contains(src, `'data-m': key`) || !strings.Contains(src, `[data-m="`) {
		t.Error("the meters carry no stable hook, so paintLive cannot write into them")
	}
}

// blockOf returns the source from the opening of a block to the string that
// closes it, and fails the test when either end is missing.
//
// Assertions about one function are made against that function rather than
// against the whole file. A substring search over a file cannot tell a value
// being compared from the same value being drawn a few lines away, and the
// difference between those two is the entire subject of these guards.
//
// It is not `between` from assets_test.go, which answers "" when an anchor is
// missing. That is the right answer where a caller then asserts on the result,
// and the wrong one here: these guards assert that something is *absent*, so an
// empty slice passes every check while testing nothing at all. A guard that
// silently stops guarding is worse than no guard, so this one stops the test
// instead.
func blockOf(t *testing.T, src, open, shut string) string {
	t.Helper()
	i := strings.Index(src, open)
	if i < 0 {
		t.Fatalf("%q is no longer in the source; this guard needs updating", open)
	}
	rest := src[i:]
	j := strings.Index(rest, shut)
	if j < 0 {
		t.Fatalf("%q is never closed by %q; this guard needs updating", open, shut)
	}
	return rest[:j]
}

// The first paint does not wait on the fleet.
//
// Every card asks its server whether it is up, and asking means an SSH
// connection. Four servers meant the page stood empty for four or five seconds
// — for a name, an address and a version that were all already on disk.
func TestTheFleetPageDrawsBeforeItAsksAnything(t *testing.T) {
	loadPanel()

	js, err := fs.ReadFile(panelRoot, "js/views/servers.js")
	if err != nil {
		t.Fatalf("cannot read servers.js: %v", err)
	}
	if !strings.Contains(string(js), "api.nodesCached()") {
		t.Error("the page's first paint is the live listing, so it cannot draw until " +
			"the slowest server in the fleet has answered")
	}

	api, err := fs.ReadFile(panelRoot, "js/api.js")
	if err != nil {
		t.Fatalf("cannot read api.js: %v", err)
	}
	if !strings.Contains(string(api), "cached=1") {
		t.Error("api.js has no call for the cached listing")
	}
}

// A probe that got no reply is not the same as a server dropping every packet,
// and the card must not say it is.
//
// ICMP is blocked outright on plenty of hosts and inside plenty of containers.
// ping reports both as 100% loss, so printing that figure would put "100% loss"
// on a card whose server is working perfectly — a confident, wrong statement of
// exactly the kind this panel should not make.
func TestAnUnmeasuredPathIsNotReportedAsTotalLoss(t *testing.T) {
	loadPanel()

	b, err := fs.ReadFile(panelRoot, "js/views/servers.js")
	if err != nil {
		t.Fatalf("cannot read servers.js: %v", err)
	}
	src := string(b)

	if !strings.Contains(src, "net.measured") {
		t.Fatal("the card does not check whether the path was measured at all")
	}
	i := strings.Index(src, "function netText(")
	if i < 0 {
		t.Fatal("netText is gone; nothing decides what the network row says")
	}
	body := src[i:]
	if end := strings.Index(body, "\n  }"); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "if (!net.measured)") {
		t.Error("netText prints a figure before checking the path was measured")
	}
}

// Whatever is behind the card measures something.
//
// The street map that used to sit here was texture — honest, in its own note,
// about knowing nothing of where the server was — and it cost the card the
// height that kept a fleet of four from fitting on one screen.
//
// What is behind it now is a gauge: two rings of dots, the outer the processor
// and the inner memory, with the lit share of each ring the share in use. That
// is the whole reason it is allowed to be there, so this guards the property
// rather than the shape — dots arranged on a circle that were always fully lit
// would be the same mistake drawn differently.
func TestTheServerCardsGroundIsAGauge(t *testing.T) {
	loadPanel()

	js, err := fs.ReadFile(panelRoot, "js/views/servers.js")
	if err != nil {
		t.Fatalf("cannot read servers.js: %v", err)
	}
	if strings.Contains(string(js), "mp-field") || strings.Contains(string(js), "mp-wash") {
		t.Error("the card still draws the decorative ground")
	}
	if strings.Contains(string(js), "RACK_SVG") {
		t.Error("the rack drawing is still here and nothing uses it")
	}

	// The rings are driven by the readings, and by the live writer rather than
	// by a rebuild — the fleet grid exists to leave unchanged cards alone.
	if !strings.Contains(string(js), `data-r=`) {
		t.Error("the ring dots carry no hook, so nothing can light them from a reading")
	}
	live := blockOf(t, string(js), "function paintLive(", "\n  }")
	for _, want := range []struct{ what, needle string }{
		{"the processor ring", "light('cpu'"},
		{"the memory ring", "light('mem'"},
		{"the figure in the middle of it", "[data-gauge]"},
	} {
		if !strings.Contains(live, want.needle) {
			t.Errorf("paintLive never writes %s, so it is decoration rather than a reading",
				want.what)
		}
	}

	css, err := fs.ReadFile(panelRoot, "css/components/servers.css")
	if err != nil {
		t.Fatalf("cannot read servers.css: %v", err)
	}
	sheet := string(css)
	for _, dead := range []string{".mp-map", ".mp-field", ".mp-wash", ".sv-bg"} {
		if strings.Contains(sheet, dead) {
			t.Errorf("%s is still styled, for an element nothing draws any more", dead)
		}
	}
	// The editor's surface is the one thing on this card that must survive a
	// redesign: it is what makes editing happen inside the card. Guarded in
	// full by TestEditingAServerHappensInsideItsCard; named here so a rewrite
	// that drops it fails against the redesign too.
	for _, keep := range []string{".mp7 .ed-l", ".mp7.ed7 .ed-l"} {
		if !strings.Contains(sheet, keep) {
			t.Errorf("%s went with the redesign; the editor has no surface", keep)
		}
	}
}
