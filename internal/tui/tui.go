// Package tui provides small terminal helpers (colors, prompts, banners)
// used by the interactive backpack menu. No third-party dependencies.
package tui

import (
	"bufio"
	"fmt"
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

// Info, Success, Warn, Error are convenience printers.
func Info(s string)    { Colorize(Cyan, s, false) }
func Success(s string) { Colorize(Green, s, true) }
func Warn(s string)    { Colorize(Yellow, s, false) }
func Error(s string)   { Colorize(Red, s, true) }

// Rule prints a horizontal separator.
func Rule() {
	fmt.Println(Yellow + "═══════════════════════════════════════════════════════" + Reset)
}

// Logo prints the backpack banner and version.
func Logo(version string) {
	fmt.Print(Cyan)
	fmt.Println(`
 ██████╗  █████╗  ██████╗██╗  ██╗██████╗  █████╗  ██████╗██╗  ██╗
 ██╔══██╗██╔══██╗██╔════╝██║ ██╔╝██╔══██╗██╔══██╗██╔════╝██║ ██╔╝
 ██████╔╝███████║██║     █████╔╝ ██████╔╝███████║██║     █████╔╝
 ██╔══██╗██╔══██║██║     ██╔═██╗ ██╔═══╝ ██╔══██║██║     ██╔═██╗
 ██████╔╝██║  ██║╚██████╗██║  ██╗██║     ██║  ██║╚██████╗██║  ██╗
 ╚═════╝ ╚═╝  ╚═╝ ╚═════╝╚═╝  ╚═╝╚═╝     ╚═╝  ╚═╝ ╚═════╝╚═╝  ╚═╝`)
	fmt.Print(Reset)
	fmt.Printf("%s Backpack  %s%s%s\n", Green, Yellow, version, Reset)
	fmt.Println(Cyan + " TeleGram : @BlackProtocols  |  GitHub : https://github.com/AminMGMT" + Reset)
}

// Prompt reads a trimmed line after printing label.
func Prompt(label string) string {
	fmt.Print(Cyan + label + Reset)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

// PromptDefault reads a line; if empty returns def.
func PromptDefault(label, def string) string {
	v := Prompt(fmt.Sprintf("%s [%s]: ", label, def))
	if v == "" {
		return def
	}
	return v
}

// PromptInt reads an integer with a default fallback.
func PromptInt(label string, def int) int {
	v := Prompt(fmt.Sprintf("%s [%d]: ", label, def))
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
	v := strings.ToLower(Prompt(fmt.Sprintf("%s %s: ", label, suffix)))
	if v == "" {
		return def
	}
	return v == "y" || v == "yes"
}

// Choose presents a numbered list and returns the 0-based selected index,
// or -1 if the user entered 0 (back/cancel).
func Choose(title string, options []string) int {
	Colorize(Cyan, title, true)
	fmt.Println()
	for i, opt := range options {
		fmt.Printf("  %s%d%s) %s\n", Magenta, i+1, Reset, opt)
	}
	fmt.Println()
	for {
		v := Prompt("Enter your choice (0 to go back): ")
		if v == "0" {
			return -1
		}
		n, err := strconv.Atoi(v)
		if err == nil && n >= 1 && n <= len(options) {
			return n - 1
		}
		Error(fmt.Sprintf("Invalid choice. Enter a number between 1 and %d (or 0).", len(options)))
	}
}

// PressEnter waits for the user to acknowledge.
func PressEnter() {
	fmt.Print(Gray + "\nPress Enter to continue..." + Reset)
	reader.ReadString('\n')
}
