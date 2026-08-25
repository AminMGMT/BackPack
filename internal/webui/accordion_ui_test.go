package webui

import (
	"strings"
	"testing"
)

// section returns the body of one top-level function in the page script, so an
// assertion about that function cannot be satisfied by a match somewhere else.
func section(t *testing.T, page, decl string) string {
	t.Helper()
	start := strings.Index(page, decl)
	if start < 0 {
		t.Fatalf("%s is missing from the page", decl)
	}
	body := page[start+len(decl):]
	// Top-level functions in this file end at the next one.
	if end := strings.Index(body, "\nfunction "); end > 0 {
		body = body[:end]
	}
	return body
}

// A drawer must not be left half-animated when the animation never runs.
//
// The reported failure: open Edit, open Fine Tune, close Edit, open Edit again —
// and Fine Tune will not open, for the life of the page, until a refresh. It was
// never only Fine Tune. Six of the panel's thirteen drawers wedged the same way:
// all four in the edit dialog, and the spoof and packet-carrier drawers in the
// setup form. The trigger is leaving a drawer open when its dialog closes.
//
// openEdit collapses the drawers while #editform still carries .hide, which is
// display:none. An element with no boxes runs no transitions, so the collapse
// fired no transitionend and the cleanup that transitionend was carrying — set
// hidden, drop the inline height — never ran. The drawer was left un-hidden and
// pinned to height 0, which toggleAcc reads as "already open", so every later
// click tried to close it instead. Closing it could not repair it either:
// collapsing something already at zero height transitions a value to itself,
// which fires no transitionend of its own.
func TestTheAccordionDoesNotDependOnATransitionThatMayNeverRun(t *testing.T) {
	page := string(dashboardHTML)

	if !strings.Contains(page, "function accRendered(") {
		t.Error("nothing checks whether a drawer is being laid out, so collapsing " +
			"one inside a display:none dialog wedges it")
	}
	if !strings.Contains(page, "function accSettle(") {
		t.Error("the animation cleanup has no single owner, so it can only be " +
			"reached through transitionend")
	}
	// The backstop: a transition that never runs never ends.
	settle := section(t, page, "function accSettle(")
	if !strings.Contains(settle, "setTimeout(finish") {
		t.Error("accSettle waits only on transitionend; a transition that never " +
			"starts then leaves the drawer half-animated forever")
	}

	closeBody := section(t, page, "function closeAcc(")
	if !strings.Contains(closeBody, "accRendered(b)") {
		t.Error("closeAcc animates a drawer that may not be rendered, which is the " +
			"reported wedge")
	}
	// .accb is border-box with padding, so a collapsed drawer still measures
	// 25px. Only the inline height says whether it is already collapsed.
	if !strings.Contains(closeBody, "b.style.height==='0px'") {
		t.Error("closeAcc does not notice a drawer already collapsed to zero, so it " +
			"animates a value to itself and never completes")
	}

	openBody := section(t, page, "function openAcc(")
	if !strings.Contains(openBody, "accRendered(b)") {
		t.Error("openAcc has the same dependency on a transition that may never " +
			"run, so a drawer opened while hidden stays pinned to a height")
	}
}

// Two animations must not fight over one drawer.
//
// Every step after the call that starts an animation — the frame that sets the
// target height, and the cleanup after it — has to check that the animation is
// still the current one. Without it, opening and closing in quick succession
// lets the open's queued frame land after the close finished, leaving the
// drawer hidden but pinned to a height: the same state the bug above produces.
func TestAStaleAccordionAnimationCannotClobberANewerOne(t *testing.T) {
	page := string(dashboardHTML)

	if !strings.Contains(page, "function accBegin(") {
		t.Fatal("there is no generation token, so a superseded animation still " +
			"acts on the drawer")
	}
	for _, fn := range []string{"function openAcc(", "function closeAcc("} {
		body := section(t, page, fn)
		if !strings.Contains(body, "accBegin(b)") {
			t.Errorf("%s does not claim the drawer, so it cannot invalidate an "+
				"animation already running on it", strings.TrimPrefix(fn, "function "))
		}
		if !strings.Contains(body, "if(b.dataset.accGen!==gen) return;") {
			t.Errorf("%s queues a frame that does not check whether it is still the "+
				"current animation", strings.TrimPrefix(fn, "function "))
		}
	}
	settle := section(t, page, "function accSettle(")
	if !strings.Contains(settle, "b.dataset.accGen!==gen") {
		t.Error("accSettle runs its cleanup without checking whether a newer " +
			"animation owns the drawer")
	}
}

// The edit dialog collapses its drawers before its form is shown, which is the
// first of the two ways a drawer gets collapsed while it has no boxes. The
// ordering is fine now that the helpers cope with it, but it is worth failing
// loudly if the reset itself is ever dropped.
func TestTheEditDialogStillResetsItsDrawers(t *testing.T) {
	page := string(dashboardHTML)
	for _, id := range []string{"eft", "esp", "epk", "ecn"} {
		if !strings.Contains(page, "closeAcc('"+id+"')") {
			t.Errorf("the edit dialog no longer resets the %s drawer, so it opens "+
				"showing whichever tunnel was edited last", id)
		}
	}
}

// The second way in, and the reason this was never only the edit dialog's bug.
//
// renderDrawers hides a drawer whose transport has no use for it and collapses
// it in the same breath, so the collapse runs on an element it has just made
// display:none. That wedged the spoof and packet-carrier drawers in the setup
// form as well — a form the edit dialog's ordering has nothing to do with.
func TestADrawerIsCollapsedRightAfterBeingHidden(t *testing.T) {
	page := string(dashboardHTML)
	body := section(t, page, "function renderDrawers(")

	hides := strings.Contains(body, "classList.toggle('hide',!on)")
	collapses := strings.Contains(body, "closeAcc(p+d.id)")
	if !hides || !collapses {
		t.Skip("renderDrawers no longer hides and collapses in one pass")
	}
	// It does both, so the helpers are the only thing standing between this and
	// the wedge. Guard the one property that makes it safe.
	closeBody := section(t, page, "function closeAcc(")
	if !strings.Contains(closeBody, "accRendered(b)") {
		t.Error("renderDrawers collapses a drawer it has just hidden, and closeAcc " +
			"does not check whether the drawer is rendered — the setup form's spoof " +
			"and packet drawers wedge again")
	}
}
