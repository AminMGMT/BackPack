package webui

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The report this exists for: "the overview page refreshes graphically every
// few seconds, and it is distracting."
//
// It was not the data changing. The page was written from innerHTML on every
// poll — four times a minute — so every element on it was thrown away and made
// again. Everything with an entrance animation replayed it together, the
// all-time figure counted up from nothing each time, and anything the reader
// had hovered, focused or was halfway through clicking came back as a
// different element. The tunnel cards were given a signature per card for
// exactly this reason; this screen never got the same treatment.
//
// The rule is: build the page once, then edit it. A region is replaced only
// when what it draws has changed, and the figures that move on every poll are
// written into the elements already on the page.
func TestTheOverviewIsNotRebuiltOnEveryPoll(t *testing.T) {
	loadPanel()

	b, err := fs.ReadFile(panelRoot, "js/views/overview.js")
	if err != nil {
		t.Fatalf("cannot read overview.js: %v", err)
	}
	src := string(b)

	body := src[strings.Index(src, "export function overview(ctx)"):]
	paintAt := strings.Index(body, "const paint =")
	if paintAt < 0 {
		t.Fatal("overview no longer has a paint function — this guard needs updating")
	}

	// The page may be built once, on the way in. Not on every poll.
	writes := regexp.MustCompile(`view\.innerHTML\s*=`).FindAllStringIndex(body, -1)
	if len(writes) != 1 {
		t.Errorf("overview writes view.innerHTML %d times; it must be built once", len(writes))
	}
	for _, w := range writes {
		if w[0] > paintAt {
			t.Error("the page is rebuilt from inside paint, which throws away every element " +
				"on it four times a minute and replays every animation with them")
		}
	}

	// And the poll must not re-bind handlers onto the elements it just made:
	// with a page that is no longer rebuilt, a listener added on each paint is
	// never removed, so one click ends up firing several times.
	paint := body[paintAt:]
	if strings.Contains(paint, "addEventListener") {
		t.Error("paint adds an event listener; on a page that is not rebuilt these " +
			"accumulate, and a single click fires once per poll that has happened")
	}

	// The figures that move on every poll have to be written into the elements
	// that are already there.
	if !strings.Contains(src, "const region =") {
		t.Error("overview.js no longer has a region helper, so it has no way to update " +
			"one part of the page without rebuilding all of it")
	}
	// The split bar moves on every poll and is set on the element it belongs to
	// rather than re-rendered around it. fillRow did the same for the per-tunnel
	// rows, which are on the tunnel cards now.
	if !strings.Contains(src, "style.setProperty") {
		t.Error("nothing on the overview is written in place any more, so every figure " +
			"that changes costs a rebuild of the region around it")
	}
}
