package menu

import (
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/telegram"
	"github.com/backpack/backpack/internal/webui"
)

// The lines the main menu is made of.
//
// This package is one long interactive loop and had no test file. Most of it
// cannot be tested without a terminal — but the strings it draws can be, and
// they are the whole of what an operator reads to decide what state their
// server is in. A summary line that says "on" for something that is off is
// worse than no line at all, because it is believed.

// A summary that says the alerts are on while nothing is actually watched is
// the exact shape of a line an operator trusts and should not.
func TestAlertSummarySaysWhenNothingIsWatched(t *testing.T) {
	off := alertSummaryLine(telegram.AlertConfig{Enabled: false})
	if off != "off" {
		t.Fatalf("disabled alerts read as %q", off)
	}

	enabledButEmpty := alertSummaryLine(telegram.AlertConfig{Enabled: true})
	if !strings.Contains(enabledButEmpty, "nothing is being watched") {
		t.Fatalf("alerts on with no thresholds read as %q — which an operator would take for working", enabledButEmpty)
	}
}

// Every threshold that is set has to appear, or an operator turns one on and
// the summary keeps saying it is not there.
func TestAlertSummaryNamesEveryThresholdThatIsOn(t *testing.T) {
	line := alertSummaryLine(telegram.AlertConfig{
		Enabled: true, CPUPercent: 85, MemPercent: 90, DiskPercent: 95,
		TunnelDown: true, NewRelease: true,
	})
	for _, want := range []string{"cpu 85%", "ram 90%", "disk 95%", "tunnel up/down", "new release"} {
		if !strings.Contains(line, want) {
			t.Fatalf("%q is missing from %q", want, line)
		}
	}
}

// A threshold of zero is off, and must not be printed as "cpu 0%" — which
// reads as a threshold that fires on everything.
func TestAZeroThresholdIsNotListed(t *testing.T) {
	line := alertSummaryLine(telegram.AlertConfig{Enabled: true, CPUPercent: 0, MemPercent: 90})
	if strings.Contains(line, "cpu") {
		t.Fatalf("a zero threshold was listed: %q", line)
	}
	if !strings.Contains(line, "ram 90%") {
		t.Fatalf("the threshold that is set went missing: %q", line)
	}
}

// The panel path is not authentication, and the line has to say what it
// actually buys — which is nothing at all when the panel is at the root.
func TestThePanelPathLineSaysWhenThereIsNoPath(t *testing.T) {
	bare := panelPathDesc(webui.Config{})
	if !strings.Contains(bare, "anyone scanning") {
		t.Fatalf("a panel at the root reads as %q, which does not say what that means", bare)
	}
}

// The three certificate states are three different things to do next, so they
// must not read alike.
func TestThePanelCertificateLineDistinguishesTheThreeStates(t *testing.T) {
	plain := panelCertDesc(webui.Config{HTTPS: false})
	self := panelCertDesc(webui.Config{HTTPS: true})
	acme := panelCertDesc(webui.Config{HTTPS: true, TLSDomain: "panel.example.ir"})

	if plain == self || self == acme || plain == acme {
		t.Fatalf("two certificate states read the same:\n  %q\n  %q\n  %q", plain, self, acme)
	}
	if !strings.Contains(plain, "no certificate") {
		t.Fatalf("plain HTTP reads as %q", plain)
	}
	if !strings.Contains(self, "self-signed") {
		t.Fatalf("a self-signed certificate reads as %q", self)
	}
	// The domain is the part that tells an operator whether the right
	// certificate is in place, so it has to be in the line.
	if !strings.Contains(acme, "panel.example.ir") {
		t.Fatalf("a Let's Encrypt certificate reads as %q without naming the domain", acme)
	}
}

// "disabled" and "every 0h" are not the same sentence, and the second one is
// not a sentence at all.
func TestTheRefreshLabelReadsAsOffWhenItIsOff(t *testing.T) {
	// AutoRefreshHours reads the machine's schedule; whatever it says, the
	// label must never render a zero or negative interval as an interval.
	got := refreshLabel()
	if strings.Contains(got, "every 0h") || strings.Contains(got, "-") {
		t.Fatalf("refreshLabel() = %q", got)
	}
}
