package manage

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/backpack/backpack/internal/tui"
)

// ConnectionTest is main-menu option 0, Connection Test: which transports work between
// this server and another. See conntest.go.
func ConnectionTest() {
	tui.Clear()
	tui.Title("Connection Test")
	fmt.Println()
	switch tui.ChooseOpt("This Server Is", []tui.Option{
		{Title: "Iran", Desc: "start here, get the link"},
		{Title: "Kharej", Desc: "paste the Iran server's link"},
	}) {
	case 0:
		connTestIranMenu()
	case 1:
		raw := strings.TrimSpace(tui.Prompt("Test Link: "))
		if raw == "" {
			return
		}
		RunConnTestKharejTUI(raw)
		tui.PressEnter()
	}
}

// connTestContext ends on Ctrl+C, so a test can be stopped and still clean up.
func connTestContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func connTestIranMenu() {
	root := os.Geteuid() == 0
	hosts := ctChooseHosts()
	if len(hosts) == 0 {
		return
	}
	presets := []string{PresetBalance, PresetTurbo, PresetAggressive}
	pick := tui.ChooseOpt("Preset", []tui.Option{
		{Title: "Balance", Desc: "least memory"},
		{Title: "Turbo", Desc: "default"},
		{Title: "Aggressive", Desc: "fast links"},
	})
	if pick < 0 {
		return
	}
	preset := presets[pick]
	tui.StopIfInputGone()

	// Every transport is tested; what needs root is left out only without it.
	// IP spoofing is checked as the IP Spoofing Tester checks it, both ways,
	// from the one forged source.
	// The sni carrier announces its default domain; nothing is asked.
	direct, spoof := root, ConnTestSpoofSource
	if !root {
		tui.Warn("Not Root — Direct, PCK And IP Spoofing Are Left Out.")
	}

	ctx, stop := connTestContext()
	defer stop()
	ctMenuStage(os.Stdout, "1", "Preparing test tunnels", "Checking listeners, helpers and REALITY cover compatibility.")
	suite, link, err := StartConnTestSuite(ConnTestOptions{Context: ctx, Direct: direct, Preset: preset, SpoofSrc: spoof}, hosts)
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	defer suite.Close()
	for _, test := range suite.tests {
		if test != nil {
			ctMenuStartup(os.Stdout, test)
		}
	}
	fmt.Println()
	ctMenuStage(os.Stdout, "2", "Connect the Kharej", "Paste this one link on Kharej. Selected IP paths run one after another.")
	fmt.Println(tui.Color(tui.Bold+tui.White, link))
	tui.Warn("Leave this test open while connecting the other server. Ctrl+C stops all paths.")
	board := &ctPathBoard{}
	paths := suite.Run(ctx, func(paths []ConnTestPath) {
		for i, p := range paths {
			board.update(i, p)
		}
	})
	board.abandon()
	ctSuiteSummary(os.Stdout, paths)
	if ctx.Err() != nil {
		tui.Warn("Stopped.")
	}
	suite.Close()
	tui.PressEnter()

}

// RunConnTestKharejTUI runs the kharej side and prints the verdict.
func RunConnTestKharejTUI(raw string) bool {
	if a, err := parseConnTestLink(raw); err == nil && len(a.Routes) > 0 {
		ctx, stop := connTestContext()
		defer stop()
		board := &ctPathBoard{}
		paths, err := runConnTestSuiteKharej(ctx, a.Routes, os.Stdout, board.update)
		board.abandon()
		ctSuiteSummary(os.Stdout, paths)
		if err != nil {
			tui.Error(err.Error())
			return false
		}
		return true
	}

	ctx, stop := connTestContext()
	defer stop()
	fmt.Println()
	title := ""
	if a, err := parseConnTestLink(raw); err == nil {
		title = a.Host
	}
	ctMenuStage(os.Stdout, "1", "Connecting to Iran", "Fetching the test settings and starting this side's tunnels.")
	var board *ctBoard
	results, best, err := RunConnTestKharej(ctx, raw, os.Stdout, func(rows []ConnTestResult) {
		if board == nil {
			fmt.Println()
			board = newCTBoard(os.Stdout, title)
		}
		board.setAll(rows)
	})
	if err != nil {
		if board != nil {
			board.abandon()
		}
		tui.Error(err.Error())
		return false
	}
	if board == nil {
		board = newCTBoard(os.Stdout, title)
	}
	board.finish(results, best)
	return true
}
