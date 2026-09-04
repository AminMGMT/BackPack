package webui

import (
	"io/fs"
	"strings"
	"testing"
)

// A tunnel that is up and delivering into nothing must not read as healthy.
//
// This is the bug report. A reverse tunnel came up on both ends — control
// channel established, peer reported, counters moving — and every connection
// through it died one hop past the end, because nothing was listening on the
// forwarded port on the far machine. The panel showed Online, which was true
// about the tunnel and useless to the operator, and the only record of the real
// failure was a line in the other server's journal.
//
// The state stays "online" on purpose: the tunnel is up, and the watchdog reads
// that field. What changes is that the panel now has the far end's own sentence
// and says it.

// The card must not be able to show a green Online for a tunnel whose far
// service is missing.
func TestAMissingFarServiceIsNotShownAsHealthy(t *testing.T) {
	loadPanel()

	src, err := fs.ReadFile(panelRoot, "js/lib/tstate.js")
	if err != nil {
		t.Fatalf("tstate.js: %v", err)
	}
	js := string(src)

	// The one place the panel decides what a state means has to know about it.
	if !strings.Contains(js, "serviceDown") {
		t.Fatal("the panel's one interpreter of tunnel state knows nothing about a " +
			"tunnel whose far service is gone, so every screen will call it healthy")
	}
	// Each export is read from its own declaration to the end of its statement,
	// so a mention of serviceDown somewhere else in the file cannot satisfy this.
	for _, want := range []struct{ what, needle string }{
		{"the label", "stateLabel"},
		{"the colour", "stateTone"},
	} {
		decl := "export const " + want.needle
		i := strings.Index(js, decl)
		if i < 0 {
			t.Fatalf("%s is gone from tstate.js", want.needle)
		}
		rest := js[i:]
		j := strings.Index(rest, ";")
		if j < 0 {
			j = len(rest) - 1
		}
		if fn := rest[:j]; !strings.Contains(fn, "serviceDown") {
			t.Errorf("%s does not account for a missing far service:\n%s", want.what, fn)
		}
	}
}

// The card renders the sentence, rather than only changing a colour.
func TestTheCardSaysWhatIsWrong(t *testing.T) {
	loadPanel()

	src, err := fs.ReadFile(panelRoot, "js/views/dashboard.js")
	if err != nil {
		t.Fatalf("dashboard.js: %v", err)
	}
	js := string(src)
	if !strings.Contains(js, "t.serviceDown") {
		t.Error("the card never renders the far end's explanation, so the operator " +
			"is left with a colour and no reason for it")
	}
	// And the card has to repaint when it changes — the signature decides that.
	sig := between(js, "t.state,", ")")
	if !strings.Contains(sig, "serviceDown") {
		t.Error("the card's repaint signature ignores serviceDown, so a service " +
			"that goes down or comes back leaves the card as it was")
	}
}

// A tunnel whose far service is fine carries nothing, so nothing is shown.
func TestAWorkingTunnelSaysNothingAboutItsFarService(t *testing.T) {
	var info TunnelInfo
	if info.ServiceDown != "" {
		t.Error("the field is non-empty by default, so every healthy tunnel would " +
			"carry a warning")
	}
}
