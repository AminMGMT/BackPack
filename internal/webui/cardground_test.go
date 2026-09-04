package webui

import (
	"io/fs"
	"strings"
	"testing"
)

// Nothing is drawn behind the chart on its own account.
//
// The card used to carry a dotted ground under the line: a 14px grid of light
// circles, masked to fade in from the left. It measured nothing — the spacing
// is fixed pixels, so the dots line up with no value on either axis — and on a
// dark card a field of light dots is the brightest thing on it, competing with
// the line that is the actual reading.
//
// The wash stays. It is the tunnel's own state colour and fades behind the
// figure rather than sitting on top of it.
func TestTheChartHasNoDecorationBehindIt(t *testing.T) {
	loadPanel()

	js, err := fs.ReadFile(panelRoot, "js/views/dashboard.js")
	if err != nil {
		t.Fatalf("dashboard.js: %v", err)
	}
	css, err := fs.ReadFile(panelRoot, "css/components/card.css")
	if err != nil {
		t.Fatalf("card.css: %v", err)
	}

	for _, gone := range []struct{ what, needle, in string }{
		{"the dotted ground", "mgrid", string(js)},
		{"its pattern", "patternUnits", string(js)},
		{"its rule", ".mgrid", string(css)},
	} {
		if strings.Contains(gone.in, gone.needle) {
			t.Errorf("%s is back on the tunnel card (%q)", gone.what, gone.needle)
		}
	}

	// And what the chart is actually made of is still there.
	for _, want := range []struct{ what, needle string }{
		{"the state wash", "mwash"},
		{"the chart", "mchart"},
	} {
		if !strings.Contains(string(js), want.needle) {
			t.Errorf("%s went with the decoration", want.what)
		}
	}
}
