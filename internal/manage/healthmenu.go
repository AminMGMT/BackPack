package manage

import (
	"fmt"
	"strings"

	"github.com/backpack/backpack/internal/tui"
)

// HealthCheck runs every diagnostic and prints a grouped report with a ✓/!/✗
// beside each item and a plain-language fix under anything that needs one.
func HealthCheck() {
	tui.Clear()
	tui.Title("Health Check")
	tui.Warn("Checking the server, the web panel and every tunnel...")
	fmt.Println()

	checks := Diagnose()
	okCount, warnCount, failCount := CountByLevel(checks)

	group := ""
	for _, c := range checks {
		if c.Group != group {
			group = c.Group
			fmt.Println()
			fmt.Printf("  %s%s%s\n", tui.Bold+tui.White, group, tui.Reset)
		}
		fmt.Printf("   %s %-22s %s%s%s\n",
			checkMark(c.Level), c.Name, tui.Gray, c.Detail, tui.Reset)
		if c.Fix != "" {
			fmt.Printf("       %s→ %s%s\n", tui.Red, c.Fix, tui.Reset)
		}
	}

	fmt.Println()
	tui.Rule()
	summary := fmt.Sprintf("%d passed", okCount)
	if warnCount > 0 {
		summary += fmt.Sprintf(" · %d warning(s)", warnCount)
	}
	if failCount > 0 {
		summary += fmt.Sprintf(" · %d problem(s)", failCount)
	}
	switch {
	case failCount > 0:
		tui.Error("Problems found — " + summary)
		tui.Warn("Follow the red suggestions above, then run the check again.")
	case warnCount > 0:
		tui.Info("Mostly healthy — " + summary)
	default:
		tui.Success("Everything looks healthy — " + summary)
	}
	tui.PressEnter()
}

// checkMark renders the status symbol for a check level.
func checkMark(l CheckLevel) string {
	switch l {
	case CheckOK:
		return tui.Color(tui.Bold+tui.White, "✓")
	case CheckWarn:
		return tui.Color(tui.Gray, "!")
	case CheckFail:
		return tui.Color(tui.Bold+tui.Red, "✗")
	default:
		return tui.Color(tui.Gray, "·")
	}
}

// FileLocations prints every path Backpack uses with a ✓/✗ so the user can see
// what is installed and where everything lives.
func FileLocations() {
	tui.Clear()
	tui.Title("File Locations")
	tui.Warn("Everything Backpack owns on this server.")
	fmt.Println()

	locs := Locations()
	width := 0
	for _, l := range locs {
		if len(l.Label) > width {
			width = len(l.Label)
		}
	}
	missing := 0
	for _, l := range locs {
		mark := tui.Color(tui.Bold+tui.White, "✓")
		if !l.Exists {
			mark = tui.Color(tui.Red, "✗")
			missing++
		}
		fmt.Printf("   %s %-*s  %s%s%s\n", mark, width, l.Label, tui.Gray, l.Path, tui.Reset)
	}

	fmt.Println()
	if missing > 0 {
		tui.Warn(fmt.Sprintf("%d item(s) not present — that is normal for features you don't use.", missing))
	}
	tui.PressEnter()
}

// transportLabel returns a friendly "TCP — plain & fast" style label.
func transportLabel(value string) string {
	for _, g := range transportGroups {
		for _, e := range g.entries {
			if e.value == value {
				return e.label
			}
		}
	}
	return strings.ToUpper(value)
}
