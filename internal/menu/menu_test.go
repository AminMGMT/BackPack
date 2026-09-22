package menu

import (
	"fmt"
	"os"
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

// A menu whose options and cases have drifted apart.
//
// manageMenu is a list of options and a switch on the index the user chose. The
// two are held together by nothing but counting, so inserting an item in the
// middle means renumbering every case after it by hand — which is exactly what
// adding "Set up from a link" required.
//
// Half of that is already safe: a *duplicate* case is a compile error, and
// writing one is how the mistake was noticed. The other half is not. A
// **missing** case compiles perfectly and falls through to the default, which
// in this switch returns to the previous screen — so the operator picks an item
// and the menu simply redraws, with no error, no log line and nothing to
// suggest the item was ever meant to do something.
//
// That is the shape this guards. It also catches the opposite: a case left
// behind when its menu item was removed, which is a screen nobody can reach.
func TestTheManageMenuHasACaseForEveryOption(t *testing.T) {
	body, err := packageSource()
	if err != nil {
		t.Fatalf("reading the package: %v", err)
	}

	start := strings.Index(body, "func manageMenu()")
	if start < 0 {
		t.Fatal("manageMenu is gone — this guard needs updating")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of manageMenu")
	}
	fn := body[start : start+end]

	options := strings.Count(fn, "{Title:")
	if options < 10 {
		t.Fatalf("found %d options — the pattern has stopped matching", options)
	}

	// Every index from 0 to options-1 must be handled exactly once.
	for i := 0; i < options; i++ {
		label := fmt.Sprintf("case %d:", i)
		switch n := strings.Count(fn, label); {
		case n == 0:
			t.Errorf("option %d (%q) has no case — picking it falls through to the "+
				"default and returns to the previous screen with no explanation",
				i, nthOption(fn, i))
		case n > 1:
			// Unreachable in practice — the compiler rejects a duplicate
			// constant case before this runs — but counted rather than assumed,
			// because the day this switch stops being a switch on a constant is
			// the day that stops being true.
			t.Errorf("option %d has %d cases", i, n)
		}
	}

	// And nothing beyond the list, which would be a case for an option that was
	// removed and a screen nobody can reach.
	if strings.Contains(fn, fmt.Sprintf("case %d:", options)) {
		t.Errorf("there is a case %d but only %d options; an action was left behind "+
			"when its menu item went", options, options)
	}
}

// nthOption pulls the title of the nth option out, for the error message.
func nthOption(fn string, n int) string {
	parts := strings.Split(fn, "{Title: ")
	if n+1 >= len(parts) {
		return "?"
	}
	rest := parts[n+1]
	if i := strings.Index(rest, `"`); i >= 0 {
		rest = rest[i+1:]
		if j := strings.Index(rest, `"`); j >= 0 {
			return rest[:j]
		}
	}
	return "?"
}

// packageSource concatenates every non-test file in the package.
//
// The screens live one per file now, so a guard that reads the source has to
// look at the package rather than at a filename — otherwise moving a screen
// into its own file silently disarms the test that watches it.
func packageSource() (string, error) {
	entries, err := os.ReadDir(".")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			return "", err
		}
		b.Write(src)
		b.WriteString("\n")
	}
	return b.String(), nil
}
