package manage

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
	host := strings.TrimSpace(tui.PromptDefault("This Server's IP", linkHost()))
	if host == "" {
		tui.Error("An address is required.")
		tui.PressEnter()
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
	realityTarget := strings.TrimSpace(tui.Prompt("REALITY Cover Endpoint (TLS 1.3/H2 Host:Port; Empty = Skip): "))
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
	tui.Info("Starting The Test Tunnels...")
	s, link, err := StartConnTestIran(ConnTestOptions{Context: ctx, Host: host, Direct: direct,
		Preset: preset, SpoofSrc: spoof, RealityTarget: realityTarget})
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	defer s.Close()

	fmt.Println()
	tui.Info("Test Link (On The Kharej: sudo backpack → 0 → Kharej):")
	fmt.Println(tui.Color(tui.Bold+tui.White, link))
	fmt.Println()
	tui.Warn(fmt.Sprintf("Waiting For The Kharej (Up To %d Min, Ctrl+C Stops).", int(connTestJoinWait.Minutes())))

	select {
	case <-s.Joined():
	case <-ctx.Done():
		tui.Warn("Stopped.")
		tui.PressEnter()
		return
	case <-time.After(connTestJoinWait):
		tui.Error("The kharej never checked in — nothing reached port " +
			fmt.Sprint(s.link.Coord) + " — neither over TCP nor UDP.")
		tui.PressEnter()
		return
	}

	tui.Success(fmt.Sprintf("Kharej %s Joined — Testing (%s, About 3 Min)...",
		s.Kharej(), preset))
	fmt.Println()
	board := newCTBoard(os.Stdout, s.Kharej())
	results := s.Run(ctx, board.set)
	board.finish(results, s.Best())
	fmt.Println()

	// The kharej collects the verdict on its next question, a few seconds
	// away; the tunnels stay up until then so it is not told they all failed.
	select {
	case <-s.Fetched():
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
	}
	stop()
	tui.Info("Stopping The Test Tunnels...")
	s.Close()
	tui.PressEnter()
}

// RunConnTestKharejTUI runs the kharej side and prints the verdict.
func RunConnTestKharejTUI(raw string) bool {
	ctx, stop := connTestContext()
	defer stop()
	fmt.Println()
	title := ""
	if a, err := parseConnTestLink(raw); err == nil {
		title = a.Host
	}
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
