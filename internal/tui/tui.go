// Package tui provides small terminal helpers (colors, prompts, banners)
// used by the interactive backpack menu. No third-party dependencies.
//
// The theme uses three colors only: red (accents, numbers, errors), white
// (titles, values) and gray (descriptions, separators).
package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// ANSI color codes.
const (
	Reset   = "\033[0m"
	Bold    = "\033[1m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	White   = "\033[37m"
	Gray    = "\033[90m"
)

var reader = bufio.NewReader(os.Stdin)

// SetInput replaces the source every prompt reads from, and returns the
// function that puts it back.
//
// It exists so the menus can be driven. `internal/menu` is a package of screens
// that each read a choice and act on it, and the only way to exercise one
// without a person at a keyboard is to hand it the keystrokes. Without this
// seam the whole package is untestable — which is exactly what it was, at 3.2%
// — and the screens are where an operator meets this product.
//
// It is not concurrency-safe and is not meant to be: prompts are read by one
// goroutine because there is one terminal.
func SetInput(r io.Reader) (restore func()) {
	prev := reader
	reader = bufio.NewReader(r)
	return func() { reader = prev }
}

// Clear clears the terminal screen.
func Clear() {
	fmt.Print("\033[H\033[2J")
}

// Color wraps s in an ANSI color and resets afterwards.
func Color(color, s string) string {
	return color + s + Reset
}

// Colorize prints a colored (optionally bold) line.
func Colorize(color, s string, bold bool) {
	if bold {
		fmt.Println(Bold + color + s + Reset)
	} else {
		fmt.Println(color + s + Reset)
	}
}

// Title prints a bold red section title.
func Title(s string) {
	Colorize(Red, s, true)
}

// Info, Success, Warn, Error are convenience printers (white / bold white /
// gray / bold red, matching the three-color theme).
func Info(s string)    { Colorize(White, s, false) }
func Success(s string) { Colorize(White, s, true) }
func Warn(s string)    { Colorize(Gray, s, false) }
func Error(s string)   { Colorize(Red, s, true) }

// Rule prints a horizontal separator.
func Rule() {
	fmt.Println(Gray + "═══════════════════════════════════════════════════════" + Reset)
}

// Logo prints the backpack banner and version.
func Logo(version string) {
	fmt.Print(Red)
	fmt.Println(`
 ██████╗  █████╗  ██████╗██╗  ██╗██████╗  █████╗  ██████╗██╗  ██╗
 ██╔══██╗██╔══██╗██╔════╝██║ ██╔╝██╔══██╗██╔══██╗██╔════╝██║ ██╔╝
 ██████╔╝███████║██║     █████╔╝ ██████╔╝███████║██║     █████╔╝
 ██╔══██╗██╔══██║██║     ██╔═██╗ ██╔═══╝ ██╔══██║██║     ██╔═██╗
 ██████╔╝██║  ██║╚██████╗██║  ██╗██║     ██║  ██║╚██████╗██║  ██╗
 ╚═════╝ ╚═╝  ╚═╝ ╚═════╝╚═╝  ╚═╝╚═╝     ╚═╝  ╚═╝ ╚═════╝╚═╝  ╚═╝`)
	fmt.Print(Reset)
	fmt.Printf("%s Backpack  %s%s%s\n", Bold+White, Red, version, Reset)
	fmt.Println(Gray + " TeleGram : @BlackProtocols  |  GitHub : https://github.com/AminMGMT" + Reset)
	// The attribution NOTICE requires a modified version to keep. Printed here
	// because this banner is the program's About screen, and that is one of the
	// places the additional term under AGPL-3.0 §7(b) names.
	fmt.Println(Gray + " " + attribution + Reset)
}

// attribution is set from app.Attribution by the caller, so this package does
// not import app — tui is the bottom of the dependency order and everything
// imports it.
var attribution = "Based on BackPack by Amin Mohammadi (AminMGMT)"

// SetAttribution lets the binary hand tui the single definition of the line,
// rather than tui carrying a second copy that can drift from it.
func SetAttribution(s string) {
	if s != "" {
		attribution = s
	}
}

// Prompt reads a trimmed line after printing label.
func Prompt(label string) string {
	v, _ := promptLine(label)
	return v
}

// promptLine is Prompt with the one thing Prompt throws away: whether there is
// any input left.
//
// It matters in exactly one place and it matters a lot there. readChoice loops
// until it is given a valid number, and a read error returns an empty string
// for ever — so on a session whose stdin has closed (a piped invocation, a
// terminal that went away, stdin from /dev/null) the menu span a tight loop
// printing "Invalid choice" until somebody killed it, burning a core and
// filling the terminal.
//
// Callers that genuinely want "empty means the default" are unaffected: they
// read the string and ignore the second value, which is what Prompt does for
// them.
func promptLine(label string) (string, bool) {
	fmt.Print(White + label + Reset)
	line, err := reader.ReadString('\n')
	trimmed := strings.TrimSpace(line)
	// A final line with no newline is still a line; only an error with nothing
	// on it means the input is gone.
	if err != nil && trimmed == "" {
		return "", false
	}
	return trimmed, true
}

// PromptDefault reads a line; if empty returns def.
func PromptDefault(label, def string) string {
	v := Prompt(fmt.Sprintf("%s %s[%s]%s: ", label, Gray, def, Reset+White))
	if v == "" {
		return def
	}
	return v
}

// PromptInt reads an integer with a default fallback.
func PromptInt(label string, def int) int {
	v := Prompt(fmt.Sprintf("%s %s[%d]%s: ", label, Gray, def, Reset+White))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// Confirm asks a yes/no question. def is returned on empty input.
func Confirm(label string, def bool) bool {
	suffix := "(y/N)"
	if def {
		suffix = "(Y/n)"
	}
	v := strings.ToLower(Prompt(fmt.Sprintf("%s %s%s%s: ", label, Gray, suffix, Reset+White)))
	if v == "" {
		return def
	}
	return v == "y" || v == "yes"
}

// Option is one selectable menu entry: a white title plus a gray description
// printed beside it.
type Option struct {
	Title string
	Desc  string
}

// ChooseOpt presents a numbered list of options with gray descriptions and
// returns the 0-based selected index, or -1 if the user entered 0 (back).
func ChooseOpt(title string, opts []Option) int {
	Colorize(Red, title, true)
	fmt.Println()
	width := 0
	for _, o := range opts {
		if len(o.Title) > width {
			width = len(o.Title)
		}
	}
	for i, o := range opts {
		num := fmt.Sprintf("%s%2d)%s", Red, i+1, Reset)
		if o.Desc == "" {
			fmt.Printf("  %s %s%s%s\n", num, Bold+White, o.Title, Reset)
			continue
		}
		fmt.Printf("  %s %s%-*s%s  %s%s%s\n",
			num, Bold+White, width, o.Title, Reset, Gray, o.Desc, Reset)
	}
	fmt.Println()
	return readChoice(len(opts))
}

// readChoice reads a 1..n selection (0 = back → -1).
func readChoice(n int) int {
	for {
		v, ok := promptLine(Gray + "Enter your choice (0 to go back): " + Reset + White)
		if !ok {
			// No input left. Going back is the only safe reading: the
			// alternative is this loop, which used to run for ever.
			return -1
		}
		if v == "0" {
			return -1
		}
		c, err := strconv.Atoi(v)
		if err == nil && c >= 1 && c <= n {
			return c - 1
		}
		Error(fmt.Sprintf("Invalid choice. Enter a number between 1 and %d (or 0).", n))
	}
}

// PressEnter waits for the user to acknowledge.
func PressEnter() {
	fmt.Print(Gray + "\nPress Enter to continue..." + Reset)
	reader.ReadString('\n')
}
