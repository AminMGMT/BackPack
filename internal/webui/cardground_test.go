package webui

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

// What is drawn behind and around the chart, and on what terms.
//
// The card carries a dotted ground under the line: a 14px grid of faint
// circles. It was taken out once, for a reason that was real — it measures
// nothing (the spacing is fixed pixels, so the dots line up with no value on
// either axis), and on a dark card an unmasked field of light dots is the
// brightest thing on it, competing with the line that is the actual reading.
//
// It is back because the design it belongs to reads as unfinished without it,
// and it is back under the two conditions that answer that objection: it is
// masked so it fades in under the chart and never reaches the text, and it is
// faint enough to read as ground rather than as a scale. Those conditions are
// the point of this test — the ground on its own was never the problem.
func TestTheChartsGroundIsMaskedAndFaint(t *testing.T) {
	loadPanel()

	js, err := fs.ReadFile(panelRoot, "js/views/dashboard.js")
	if err != nil {
		t.Fatalf("dashboard.js: %v", err)
	}
	css, err := fs.ReadFile(panelRoot, "css/components/card.css")
	if err != nil {
		t.Fatalf("card.css: %v", err)
	}

	if !strings.Contains(string(js), "mgrid") {
		t.Error("the tunnel card draws no dotted ground")
	}
	for _, want := range []struct{ what, needle string }{
		{"the state wash", "mwash"},
		{"the chart", "mchart"},
	} {
		if !strings.Contains(string(js), want.needle) {
			t.Errorf("%s is missing from the card", want.what)
		}
	}

	rule := ruleOf(t, string(css), ".c7 .mgrid{")

	// Masked, or it is decoration sitting behind the words.
	if !strings.Contains(rule, "mask-image") {
		t.Error("the dotted ground is not masked, so it runs under the card's text " +
			"instead of fading in under the chart")
	}

	// Faint, or it outshines the line it sits behind.
	op := opacityOf(t, rule)
	if op > 0.2 {
		t.Errorf("the dotted ground is at opacity %.2f — above 0.2 it competes with "+
			"the line, which is the reading", op)
	}

	// Drawn as a background, not an SVG pattern. tokens.css sets svg{fill:none}
	// for the icon set and a CSS declaration beats a presentation attribute, so
	// a <rect fill="url(#pattern)"> would be painted with no fill at all.
	if strings.Contains(string(js), "patternUnits") {
		t.Error("the ground is drawn as an SVG pattern; svg{fill:none} in tokens.css " +
			"means its rect would render empty")
	}
}

// Nothing in the chart is stroked on its own account.
//
// tokens.css sets svg{stroke:currentColor} for the icon set, and fill and
// stroke both inherit in SVG. The area path asks only for a fill, so it was
// inheriting that stroke and outlining itself in the card's text colour —
// drawing a white line down the right of the chart, across the bottom and back
// up the left, which is the closing edge of the filled shape. It read as an
// axis and was a mistake; the bars had the same outline for the same reason.
func TestOnlyTheLineAndBarsAreStroked(t *testing.T) {
	loadPanel()

	css, err := fs.ReadFile(panelRoot, "css/components/card.css")
	if err != nil {
		t.Fatalf("card.css: %v", err)
	}

	if !strings.Contains(string(css), ".c7 .mchart{stroke:none}") {
		t.Error("the chart does not clear the inherited stroke, so the filled area " +
			"outlines itself in the card's text colour")
	}
	// And the two elements that are meant to carry one still do.
	if !strings.Contains(string(css), ".c7 .mchart path[stroke]{stroke:var(--mc)}") {
		t.Error("the line no longer takes the metric's colour")
	}
}

// ruleOf returns the body of the first CSS rule opening with head. It fails
// rather than returning empty: a guard that silently passes because it could
// not find what it was checking is worse than no guard.
func ruleOf(t *testing.T, css, head string) string {
	t.Helper()
	i := strings.Index(css, head)
	if i < 0 {
		t.Fatalf("no %q rule in card.css", head)
	}
	rest := css[i+len(head):]
	j := strings.Index(rest, "}")
	if j < 0 {
		t.Fatalf("the %q rule is never closed", head)
	}
	return rest[:j]
}

// opacityOf reads the opacity declaration out of a rule body.
func opacityOf(t *testing.T, rule string) float64 {
	t.Helper()
	i := strings.Index(rule, "opacity:")
	if i < 0 {
		t.Fatal("the dotted ground sets no opacity, so nothing keeps it faint")
	}
	rest := rule[i+len("opacity:"):]
	end := strings.IndexAny(rest, ";}\n\r\t ")
	if end < 0 {
		end = len(rest)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(rest[:end]), 64)
	if err != nil {
		t.Fatalf("cannot read the ground's opacity %q: %v", rest[:end], err)
	}
	return v
}
