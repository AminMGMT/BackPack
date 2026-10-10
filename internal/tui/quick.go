package tui

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

var quickDefaults bool
var inputFile = os.Stdin

// QuickDefaults enables Space/Enter to accept the displayed suggestion. Typing
// any other character starts a normal editable answer, completed with Enter.
// Its scope is one wizard; ordinary menus retain their existing behavior.
func QuickDefaults() func() {
	previous := quickDefaults
	quickDefaults = true
	return func() { quickDefaults = previous }
}

func QuickDefaultsEnabled() bool { return quickDefaults }

func defaultLine(label string) string {
	if !quickDefaults {
		return Prompt(label)
	}
	if inputFile == nil {
		v := Prompt(label)
		StopIfInputGone()
		return v
	}
	restore, ok := beginQuickInput(inputFile)
	if !ok {
		v := Prompt(label)
		StopIfInputGone()
		return v
	}
	// Restore before invoking the input-loss handler, which may exit the app.
	fmt.Print(White + label + Reset)
	var answer []byte
	for {
		b, err := reader.ReadByte()
		if err != nil {
			restore()
			fmt.Println()
			inputEnded = true
			StopIfInputGone()
			return ""
		}
		if b == '\n' || b == '\r' || b == ' ' && len(answer) == 0 {
			restore()
			fmt.Println()
			return strings.TrimSpace(string(answer))
		}
		if b == 3 || b == 4 && len(answer) == 0 { // Ctrl+C / Ctrl+D
			restore()
			fmt.Println()
			inputEnded = true
			StopIfInputGone()
			return ""
		}
		if b == 127 || b == 8 {
			if len(answer) > 0 {
				// Remove a complete UTF-8 character when editing a pasted name.
				_, size := utf8.DecodeLastRune(answer)
				answer = answer[:len(answer)-size]
				fmt.Print("\b \b")
			}
			continue
		}
		if b >= 32 && b != 127 {
			answer = append(answer, b)
			_, _ = os.Stdout.Write([]byte{b})
		}
	}
}

// ChooseOptDefault presents an explicit recommended option. Closed input goes
// back rather than accepting the suggestion, including when driven by a pipe.
func ChooseOptDefault(title string, opts []Option, def int) int {
	if len(opts) == 0 {
		return -1
	}
	if def < 0 || def >= len(opts) {
		return ChooseOpt(title, opts)
	}
	renderOptions(title, opts)
	for {
		v := defaultLine(fmt.Sprintf("Choice [%d] (Space/Enter = keep, 0 = back): ", def+1))
		if inputEnded || v == "0" {
			return -1
		}
		if v == "" {
			return def
		}
		var n int
		if _, err := fmt.Sscan(v, &n); err == nil && fmt.Sprint(n) == v && n >= 1 && n <= len(opts) {
			return n - 1
		}
		Error("Choose a listed number, or press Space/Enter to keep the suggestion.")
	}
}
