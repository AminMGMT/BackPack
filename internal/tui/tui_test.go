package tui

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"
)

// The input helpers, which decide what a wizard answer means.
//
// These look trivial and they are the layer where an empty line has to keep
// meaning "the default" and a typo has to not become a value. Every setup the
// CLI writes goes through them, and getting Confirm's default wrong would flip
// an answer nobody gave — which is exactly the class of thing that produces a
// tunnel configured in a way the operator did not choose.

// drive points the package's reader at a script of answers and puts stdout
// somewhere nobody has to look at.
func drive(t *testing.T, input string) func() {
	t.Helper()
	oldReader, oldStdout := reader, os.Stdout
	reader = bufio.NewReader(strings.NewReader(input))
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("cannot open %s: %v", os.DevNull, err)
	}
	os.Stdout = devnull
	return func() {
		reader, os.Stdout = oldReader, oldStdout
		devnull.Close()
	}
}

func TestAnEmptyAnswerTakesTheDefault(t *testing.T) {
	defer drive(t, "\n\n\n")()

	if got := PromptDefault("name", "server-443"); got != "server-443" {
		t.Errorf("PromptDefault on an empty line = %q, want the default", got)
	}
	if got := PromptInt("port", 8443); got != 8443 {
		t.Errorf("PromptInt on an empty line = %d, want the default", got)
	}
	if got := Confirm("listen on IPv6 as well", false); got != false {
		t.Error("Confirm on an empty line did not take its default")
	}
}

func TestAnAnswerOverridesTheDefault(t *testing.T) {
	defer drive(t, "my-tunnel\n2096\n")()

	if got := PromptDefault("name", "server-443"); got != "my-tunnel" {
		t.Errorf("PromptDefault = %q, want the typed answer", got)
	}
	if got := PromptInt("port", 8443); got != 2096 {
		t.Errorf("PromptInt = %d, want 2096", got)
	}
}

// A number that is not a number falls back rather than becoming zero. Zero is a
// port the kernel chooses, which a peer cannot dial — so silently accepting it
// would build a tunnel nobody can reach.
func TestAnUnparseableNumberFallsBackToTheDefault(t *testing.T) {
	defer drive(t, "not-a-number\n")()

	if got := PromptInt("port", 8443); got != 8443 {
		t.Errorf("PromptInt on %q = %d, want the default rather than 0", "not-a-number", got)
	}
}

// Confirm's defaults are the other way round on the two prompts, and an answer
// has to beat either. "yes" and "y" both count; anything else is no.
func TestConfirmReadsTheAnswerRatherThanGuessing(t *testing.T) {
	for _, tc := range []struct {
		typed string
		def   bool
		want  bool
	}{
		{"y\n", false, true},
		{"Y\n", false, true},
		{"yes\n", false, true},
		{"YES\n", false, true},
		{"n\n", true, false},
		{"no\n", true, false},
		{"maybe\n", true, false}, // anything unrecognised is not consent
		{"\n", true, true},
		{"\n", false, false},
	} {
		func() {
			defer drive(t, tc.typed)()
			if got := Confirm("question", tc.def); got != tc.want {
				t.Errorf("Confirm(%q, default %v) = %v, want %v",
					strings.TrimSpace(tc.typed), tc.def, got, tc.want)
			}
		}()
	}
}

// Whitespace around an answer is the operator's, not the value's.
func TestSurroundingSpaceIsTrimmed(t *testing.T) {
	defer drive(t, "   spaced-name   \n")()
	if got := Prompt("name: "); got != "spaced-name" {
		t.Errorf("Prompt = %q, want the answer without its padding", got)
	}
}

// ChooseOpt returns a zero-based index, and 0 means back — an off-by-one here
// would run the wrong menu entry.
func TestChoosingAnOptionReturnsItsIndex(t *testing.T) {
	opts := []Option{{Title: "first"}, {Title: "second"}, {Title: "third"}}

	for _, tc := range []struct {
		typed string
		want  int
	}{
		{"1\n", 0},
		{"3\n", 2},
		{"0\n", -1}, // back
		{"9\n", -1}, // out of range
		{"x\n", -1}, // not a number
		{"\n", -1},  // nothing typed
	} {
		func() {
			defer drive(t, tc.typed)()
			if got := ChooseOpt("pick", opts); got != tc.want {
				t.Errorf("ChooseOpt with %q = %d, want %d", strings.TrimSpace(tc.typed), got, tc.want)
			}
		}()
	}
}

// Input that has run out must end the menu, not spin.
//
// readChoice loops until it is given a valid number, and Prompt returned an
// empty string for ever once the reader failed — so a session whose stdin had
// closed (a piped invocation, a terminal that went away, stdin from /dev/null)
// put the menu in a tight loop printing "Invalid choice" until somebody killed
// it, burning a core.
//
// This test is the reason that was found: written against the old code, it
// never returned.
func TestClosedInputEndsTheMenuRatherThanSpinning(t *testing.T) {
	defer drive(t, "")() // nothing at all to read

	done := make(chan int, 1)
	go func() {
		done <- ChooseOpt("pick", []Option{{Title: "only"}})
	}()

	select {
	case got := <-done:
		if got != -1 {
			t.Errorf("ChooseOpt on closed input = %d, want -1 (back)", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ChooseOpt never returned with no input left; it is spinning")
	}
}

// The main menu reads a free-form answer and redraws on anything else, so it
// needs to be told the input is gone: an empty answer there is "Invalid option"
// and another lap. A wizard driven from a file wrote 3.6 GB of those laps.
func TestPromptOrEndTellsAnEmptyLineFromNoInput(t *testing.T) {
	defer drive(t, "\n")()
	if v, ok := PromptOrEnd("x: "); v != "" || !ok {
		t.Errorf("an empty line = (%q, %v), want (\"\", true)", v, ok)
	}
	if _, ok := PromptOrEnd("x: "); ok {
		t.Error("PromptOrEnd with no input left said there was more; the main menu would spin")
	}
}

// And a line that arrives without a trailing newline is still a line — the last
// answer of a piped script has no newline after it.
func TestAFinalLineWithoutANewlineIsStillRead(t *testing.T) {
	defer drive(t, "2")() // no "\n"
	if got := ChooseOpt("pick", []Option{{Title: "a"}, {Title: "b"}}); got != 1 {
		t.Errorf("ChooseOpt = %d, want 1 — a final line without a newline was ignored", got)
	}
}
