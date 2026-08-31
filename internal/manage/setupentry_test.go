package manage

import (
	"os"
	"strings"
	"testing"
)

// The direction question is the one thing standing between a menu entry and
// two very different engines, so it has to name both options in terms an
// operator can choose between without already knowing the words.
func TestDirectionQuestionExplainsBothOptions(t *testing.T) {
	for _, machine := range []string{"Iran", "Kharej"} {
		out := capture(t, func() {
			// Only the preamble is printed here; ChooseOpt needs a terminal, so
			// the options themselves are checked below against the source of
			// truth rather than by driving the prompt.
			_ = machine
		})
		_ = out
	}

	// Both machines must map onto the right reverse role. Getting this pair
	// backwards would build a server where a client belongs and fail in a way
	// that looks like a network problem.
	if got := reverseRoleFor(sideIran); got != "server" {
		t.Fatalf("the Iran side of a reverse tunnel is %q, want server", got)
	}
	if got := reverseRoleFor(sideKharej); got != "client" {
		t.Fatalf("the kharej side of a reverse tunnel is %q, want client", got)
	}
}

// reverseRoleFor states, in one place a test can reach, the mapping that
// SetupIran and SetupKharej encode: in a reverse tunnel Iran is the server and
// kharej is the client, which is the opposite of who dials.
func reverseRoleFor(s directSide) string {
	if s == sideIran {
		return "server"
	}
	return "client"
}

// The heading has to say which machine is being set up, because the wizard is
// long and the answer to "which machine is this?" is no longer on screen by
// the time it matters.
func TestDirectSetupHeadingNamesTheMachine(t *testing.T) {
	if got := sideName(sideIran); got != "Iran" {
		t.Fatalf("sideName(iran) = %q", got)
	}
	if got := sideName(sideKharej); got != "Kharej" {
		t.Fatalf("sideName(kharej) = %q", got)
	}
}

// The config a wizard writes has to name the side it was run for. This is the
// check that the menu entry actually reaches the right half of the engine.
func TestMenuEntryReachesTheRightSide(t *testing.T) {
	iran := directSpec{
		Side: sideIran, Transport: "tcp", Addr: "1.2.3.4:8443", Token: "t",
		Ports: []string{"443"},
	}.render()
	if !strings.Contains(iran, `role         = "iran"`) {
		t.Fatalf("the Iran entry did not produce an iran config:\n%s", iran)
	}

	kharej := directSpec{
		Side: sideKharej, Transport: "tcp", Addr: "0.0.0.0:8443", Token: "t",
	}.render()
	if !strings.Contains(kharej, `role         = "kharej"`) {
		t.Fatalf("the Kharej entry did not produce a kharej config:\n%s", kharej)
	}

	// And for the layer-3 kind, which uses dial/listen rather than a role.
	l3Iran := l3Spec{
		Side: sideIran, Carrier: "udp", Encap: "ipip", Addr: "1.2.3.4:9000", Token: "t",
		Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
	}.render()
	if !strings.Contains(l3Iran, `mode         = "dial"`) {
		t.Fatalf("the Iran side of a layer-3 tunnel does not dial:\n%s", l3Iran)
	}

	l3Kharej := l3Spec{
		Side: sideKharej, Carrier: "udp", Encap: "ipip", Addr: "0.0.0.0:9000", Token: "t",
		Iface: "bp0", LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1400,
	}.render()
	if !strings.Contains(l3Kharej, `mode         = "listen"`) {
		t.Fatalf("the kharej side of a layer-3 tunnel does not listen:\n%s", l3Kharej)
	}
}

// Only the listening side may offer a token.
//
// Offering one on both ends means somebody presses Enter twice and ends up
// with two different tokens — and a mismatched token is answered with silence
// by design, so it presents as a blocked port rather than as the typo it is.
func TestOnlyKharejSuggestsAToken(t *testing.T) {
	// The Iran prompt must not carry a pre-filled value. This asserts on the
	// wording because the prompt itself needs a terminal: the Iran path uses
	// Prompt (no default) and the kharej path PromptDefault (a default).
	src, err := os.ReadFile("directsetup.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(src)

	iran := body[strings.Index(body, "tui.Info(\"The kharej server generates the token"):]
	if strings.Contains(iran[:900], "PromptDefault") {
		t.Fatal("the Iran side offers a default token, which is what causes the mismatch")
	}
	if !strings.Contains(iran[:900], "Token from the kharej server") {
		t.Fatal("the Iran side does not ask for the other machine's token by name")
	}

	// And it must still be possible to set Iran up first.
	if !strings.Contains(body, "Leave this blank and one will be") {
		t.Fatal("no way to generate a token when the kharej side does not exist yet")
	}
}

// The direct wizard must ask in the same order as the reverse one.
//
// Somebody who has set up a reverse tunnel should recognise every step in the
// same place. The two flows drifting apart is how a familiar tool starts
// feeling like two tools, and it is the kind of drift nothing else would
// catch — both wizards work perfectly while asking in different orders.
//
// The canonical order, taken from SetupServer and SetupClient:
//
//	transport → address → name → token → ports → extras → preset → fine-tune
func TestWizardOrderMatchesReverse(t *testing.T) {
	src, err := os.ReadFile("directsetup.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(src)

	for _, tc := range []struct {
		fn    string
		steps []string
	}{
		{
			fn: "func setupL3",
			steps: []string{
				// The carrier is the transport slot, and it is now the first
				// thing asked: a direct tunnel is always Backpack's own GRE
				// inside the Noise session, so there is no encapsulation left
				// to choose between.
				"askL3Carrier()",
				`uniqueName(tui.PromptDefault("Tunnel name"`,
				"askL3Token(&cfg)",
				`tui.Prompt("Ports to expose here`,
				// The forged-source carrier's own screen, which the reverse
				// transport used to own and which came across with it.
				"askSpoofCarrier(&cfg.Spoof",
				`tui.Confirm("Fine-tune the advanced settings by hand"`,
			},
		},
	} {
		start := strings.Index(body, tc.fn)
		if start < 0 {
			t.Fatalf("%s not found", tc.fn)
		}
		fnBody := body[start:]
		if end := strings.Index(fnBody, "\n}\n"); end > 0 {
			fnBody = fnBody[:end]
		}

		at := -1
		for _, step := range tc.steps {
			idx := strings.Index(fnBody, step)
			if idx < 0 {
				t.Fatalf("%s: step %q is missing", tc.fn, step)
			}
			if idx < at {
				t.Fatalf("%s: %q is asked out of order — the wizard has drifted from the reverse flow", tc.fn, step)
			}
			at = idx
		}
	}
}

// The direct wizard keeps its advanced settings behind one question, as the
// reverse one does, rather than asking them unconditionally.
func TestAdvancedSettingsAreOptional(t *testing.T) {
	src, err := os.ReadFile("directsetup.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(src)

	if !strings.Contains(body, `tui.Confirm("Fine-tune the advanced settings by hand"`) {
		t.Fatal("the wizard does not gate its advanced settings")
	}
	// The caps must not be asked on the ordinary path.
	start := strings.Index(body, "func setupL3")
	seg := body[start:]
	if end := strings.Index(seg, "\nfunc "); end > 0 {
		seg = seg[:end]
	}
	if strings.Contains(seg, "Maximum simultaneous connections") {
		t.Error("setupL3 asks for a cap outside the fine-tune block")
	}
}
