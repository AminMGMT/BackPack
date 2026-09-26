package menu

import (
	"bytes"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/webui"
)

// Driving the screens.
//
// This package is where an operator meets the product, and it was at 3.2%
// coverage: one long interactive loop that nothing could enter without a person
// at a keyboard. Two things made it reachable — `promptLine` learned to report
// a closed input instead of spinning on it, and `tui.SetInput` lets a test be
// the keyboard.
//
// What these tests hold is deliberately narrow and deliberately the part that
// breaks. Every screen must draw, offer the options it is supposed to offer,
// and come back when it is told to go back. None of them drives a screen into
// an action: an action here writes a config, restarts a service or reaches a
// network, and a test suite that does those things on the machine running it is
// worse than no test suite.

// drive runs a screen with input as its keystrokes and returns everything it
// printed.
//
// Two safeguards, because a screen that does not read its input is a screen
// that hangs a CI run rather than failing it: the input ends (so every prompt
// after the script runs out reads a closed stdin, which is the case
// `promptLine` was taught to handle), and the whole thing is bounded.
func drive(t *testing.T, input string, screen func()) string {
	t.Helper()

	restore := tui.SetInput(strings.NewReader(input))
	defer restore()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stdout := os.Stdout
	os.Stdout = w

	captured := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		captured <- buf.String()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		screen()
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		os.Stdout = stdout
		w.Close()
		t.Fatal("the screen never came back — it is not reading its input, or it is " +
			"waiting on something a test cannot give it")
	}

	os.Stdout = stdout
	w.Close()
	out := <-captured
	r.Close()
	return out
}

// ansi strips the colour escapes so an assertion can be about the words.
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plain(s string) string { return ansi.ReplaceAllString(s, "") }

// Every screen reachable from the main menu, entered and left again.
//
// "0" is "go back" everywhere, so this is the one input that is safe to give
// every screen: it draws itself and returns. What it proves is not much on its
// own and is exactly what nothing proved before — that each screen renders,
// with the options it is meant to have, and does not panic on the way.
//
// It is also the guard against a whole class of change that compiles: a screen
// that reads a config that has moved, indexes a slice that is now shorter, or
// dereferences something the setup no longer fills in.
func TestEveryScreenDrawsAndComesBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		screen func()
		// wants are words that must appear. They are the ones an operator
		// navigates by, so a screen that stops printing them is broken for its
		// purpose even when it runs.
		wants []string
	}{
		{"manage", manageMenu, []string{"Manage"}},
		{"backup", backupMenu, []string{"Backup"}},
		{"web panel", webPanelMenu, []string{"Web Panel"}},
		{"telegram", telegramMenu, []string{"Telegram"}},
		{"update", updateMenu, []string{"Update"}},
		{"auto refresh", autoRefreshMenu, []string{"Refresh"}},
		{"built-in proxy", builtinProxyMenu, []string{"Proxy"}},
		{"release channel", channelMenu, []string{"Release channel", "Stable", "Beta"}},
		{"uninstall", uninstallMenu, []string{"Uninstall"}},
		{"restore points", restorePointMenu, []string{"Restore points"}},
		// optimizeMenu is deliberately not here. It opens with a confirmation
		// whose default is yes, so any input that is not a refusal applies
		// sysctls to the machine running the test.
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := plain(drive(t, "0\n", tc.screen))
			if strings.TrimSpace(out) == "" {
				t.Fatal("the screen printed nothing at all")
			}
			for _, want := range tc.wants {
				if !strings.Contains(out, want) {
					t.Errorf("the screen does not mention %q; it drew:\n%s", want, out)
				}
			}
		})
	}
}

// A screen must not spin when its input is gone.
//
// This is the fault that made the package testable in the first place: a piped
// invocation, a terminal that went away or stdin from /dev/null left the choice
// loop reading an empty string for ever, printing "Invalid choice" as fast as
// the terminal would take it. Every screen inherits the fix through readChoice,
// and this is the assertion that it stays inherited — an empty script, with no
// "0" in it at all.
func TestAScreenWithNoInputLeftDoesNotSpin(t *testing.T) {
	out := plain(drive(t, "", manageMenu))
	if n := strings.Count(out, "Invalid choice"); n > 1 {
		t.Fatalf("the screen printed %q %d times on closed input", "Invalid choice", n)
	}
}

// Nonsense is rejected and then the screen carries on, rather than acting on
// whatever the number nearest the typo happened to be.
func TestABadChoiceIsRefusedAndTheScreenStays(t *testing.T) {
	out := plain(drive(t, "not a number\n0\n", manageMenu))
	if !strings.Contains(out, "Invalid choice") {
		t.Fatalf("a non-numeric choice was not refused; the screen drew:\n%s", out)
	}
}

// The main menu is the first thing anybody sees, so the list it draws is worth
// pinning: every entry numbered, in order, with nothing missing in the middle.
func TestTheMainMenuNumbersEveryEntry(t *testing.T) {
	out := plain(drive(t, "", printMenu))
	for i := 1; i <= 8; i++ {
		if !strings.Contains(out, itoa(i)+")") {
			t.Errorf("the main menu has no entry %d; it drew:\n%s", i, out)
		}
	}
}

func itoa(n int) string { return string(rune('0' + n)) }

// The two panel sub-screens take the configuration rather than reading it, so
// they can be drawn against a known one — which is the only way to assert that
// what they print is what the configuration says.
func TestThePanelSubScreensDrawTheConfigurationTheyAreGiven(t *testing.T) {
	cfg := webui.Config{Port: 7777, BasePath: "/a-secret-path", TLSDomain: "panel.example.com"}

	path := plain(drive(t, "0\n", func() { panelPathMenu(cfg) }))
	if !strings.Contains(path, "a-secret-path") {
		t.Errorf("the panel path screen does not show the path it was given:\n%s", path)
	}
	if !strings.Contains(path, "panel.example.com") {
		t.Errorf("the panel path screen does not use the certificate domain as the host:\n%s", path)
	}

	https := cfg
	https.HTTPS = true
	cert := plain(drive(t, "0\n", func() { panelCertMenu(https) }))
	if !strings.Contains(cert, "panel.example.com") {
		t.Errorf("the certificate screen does not name the domain it is for:\n%s", cert)
	}

	// And the other way: a panel served over plain HTTP must not read as
	// having a certificate for the domain it merely knows about.
	plainHTTP := plain(drive(t, "0\n", func() { panelCertMenu(cfg) }))
	if !strings.Contains(plainHTTP, "no certificate") {
		t.Errorf("a panel on plain HTTP does not say it has no certificate:\n%s", plainHTTP)
	}
}

// The relay offer is a decision, not a screen: it is what runs when the release
// host cannot be reached, and answering it wrongly is the difference between an
// update that finds another way out and one that gives up. With no tunnel to
// relay through there is nothing to offer, and it has to say so and return
// false rather than present an empty list.
func TestTheRelayOfferSaysSoWhenThereIsNoWayOut(t *testing.T) {
	var took bool
	out := plain(drive(t, "\n", func() { took = offerRelay(errors.New("dial github.com: no route to host")) }))

	if took {
		t.Fatal("the relay offer was accepted when there was nothing to relay through")
	}
	if !strings.Contains(out, "no route to host") {
		t.Errorf("the offer does not say why the update failed:\n%s", out)
	}
}

// After an update the menu used to come back as the build it was started as —
// the old version on the title, the old screens — until the operator quit and
// ran it again. It now opens the installed binary in its own place, and says
// how to do it by hand when it cannot.
func TestAnUpdateReopensTheMenuOnTheNewBinary(t *testing.T) {
	calls := 0
	prev := execSelf
	defer func() { execSelf = prev }()

	execSelf = func() error { calls++; return nil }
	drive(t, "", reopen)
	if calls != 1 {
		t.Fatalf("reopen asked to start the new binary %d times, want 1", calls)
	}

	execSelf = func() error { return errors.New("permission denied") }
	if out := drive(t, "\n", reopen); !strings.Contains(out, "run sudo backpack again") {
		t.Fatalf("a failed reopen does not say what to do:\n%s", out)
	}

	src, err := os.ReadFile("update.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, after := range []string{`tui.Success("Done.")`, `tui.Success("Backpack updated successfully.")`,
		`tui.Success("Rolled back to "`} {
		body := string(src)
		i := strings.Index(body, after)
		if i < 0 || !strings.Contains(body[i:i+200], "reopen()") {
			t.Errorf("the screen that prints %s does not reopen the menu", after)
		}
	}
}
