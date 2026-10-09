package manage

import (
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/backpack/backpack/internal/tui"
)

func ctMenuStage(out io.Writer, number, title, detail string) {
	fmt.Fprintf(out, "%s\n  %s %s\n  %s\n",
		tui.Color(tui.Gray, strings.Repeat("─", 55)),
		tui.Color(tui.Bold+tui.Red, number+"."), tui.Color(tui.Bold+tui.White, title),
		tui.Color(tui.Gray, detail))
}

func ctMenuStartup(out io.Writer, s *ConnTestIran) {
	fmt.Fprintln(out)
	row := func(mark, label, value string) {
		fmt.Fprintf(out, "  %s %s %s\n", mark, tui.Color(tui.Gray, fmt.Sprintf("%-16s", label)), tui.Color(tui.White, value))
	}
	ready, skipped := 0, 0
	for _, c := range s.cases {
		if c.skip != "" {
			skipped++
		} else {
			ready++
		}
	}
	row(checkMark(CheckOK), "Iran", s.link.Host)
	row(checkMark(CheckOK), "Preset", titleWord(s.link.Preset))
	level := CheckOK
	if skipped > 0 {
		level = CheckWarn
	}
	if ready == 0 {
		level = CheckFail
	}
	row(checkMark(level), "Test cases", fmt.Sprintf("%d configured · %d skipped", ready, skipped))
	if s.realityTarget != "" {
		row(checkMark(CheckOK), "REALITY cover", s.realityTarget+" · verified")
		fmt.Fprintln(out, tui.Color(tui.Gray, "    Cover check passed here; the two-server test starts after Kharej joins."))
		host, _, _ := net.SplitHostPort(s.realityTarget)
		if host == "www.apple.com" {
			row(checkMark(CheckWarn), "Cover note", "Apple fallback selected")
			fmt.Fprintln(out, tui.Color(tui.Gray, "    Xray cautions against Apple/iCloud targets for permanent tunnels."))
		}
		fmt.Fprintln(out, tui.Color(tui.Gray, "    Test listeners use temporary ports. Prefer port 443 for a permanent REALITY tunnel."))
	}
	coverSkipped := false
	for _, c := range s.cases {
		if c.skip != "" && managedTransport(c.tr) {
			coverSkipped = coverSkipped || c.tr == "reality"
			row(checkMark(CheckWarn), ctName(c.tr), "Skipped")
			fmt.Fprintln(out, tui.Color(tui.Gray, "    "+c.skip))
		}
	}
	if s.realityLog != "" && coverSkipped {
		fmt.Fprintln(out, tui.Color(tui.Gray, "    Probe details while this test is open: "+s.realityLog))
	}
}
