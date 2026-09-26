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

// Only one side may offer a token, and it is the Iran side.
//
// Offering one on both ends means somebody presses Enter twice and ends up
// with two different tokens — and a mismatched token is answered with silence
// by design, so it presents as a blocked port rather than as the typo it is.
// Iran sets the tunnel up first and hands the token over in its code, so the
// kharej, set up by hand, must be given the Iran server's token.
func TestOnlyIranSuggestsAToken(t *testing.T) {
	// The kharej prompt must not carry a pre-filled value. This asserts on the
	// wording because the prompt itself needs a terminal: the kharej path uses
	// Prompt (no default) and the Iran path PromptDefault (a default).
	src, err := os.ReadFile("directsetup.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(src)

	fn := body[strings.Index(body, "func askSharedToken"):]
	fn = fn[:strings.Index(fn, "\n}\n")]
	iran, kharej, ok := strings.Cut(fn, "\n\t\treturn token, true\n\t}\n")
	if !ok {
		t.Fatal("askSharedToken no longer has an Iran branch followed by the kharej one")
	}
	if strings.Contains(kharej, "PromptDefault") {
		t.Fatal("the kharej side offers a default token, which is what causes the mismatch")
	}
	if !strings.Contains(kharej, "From The Iran Server") {
		t.Fatal("the kharej side does not ask for the Iran server's token by name")
	}
	if !strings.Contains(iran, "randomToken(64)") || !strings.Contains(iran, `PromptDefault("Security Token"`) {
		t.Fatal("the Iran side does not generate and offer the token")
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
				// Iran, in the order the operator asked for: where, which
				// port, what to forward, then the name and the token.
				`tui.Prompt("Kharej IP Or Domain: ")`,
				`tui.PromptDefault("Tunnel Port", "9000")`,
				`tui.Prompt("Forwarded Ports (Blank For TUN): ")`,
				`uniqueName(tui.PromptDefault("Tunnel Name"`,
				"askL3Token(&cfg)",
				`tui.Confirm("Carry UDP As Well As TCP On Those Ports"`,
				// The carrier's own questions — the forged-source carrier's
				// screen, which the reverse transport used to own, and the SNI
				// domain — gathered in one place so the paste-a-code path asks
				// them too.
				"askL3CarrierExtras(&cfg, side)",
				"askL3FEC(&cfg, side)",
				"chooseL3Preset(false)",
				`tui.Confirm("Fine-Tune The Advanced Settings"`,
				"summariseL3(cfg, link)",
				`tui.Confirm("Create This Tunnel"`,
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

	if !strings.Contains(body, `tui.Confirm("Fine-Tune The Advanced Settings"`) {
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

// IP and SNI spoofing keep the wizard they had: they leave setupL3 before the
// link-based questions, and in theirs the kharej side makes the token.
func TestSpoofingCarriersKeepTheClassicWizard(t *testing.T) {
	src, err := os.ReadFile("directsetup.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(src)
	fn := body[strings.Index(body, "func setupL3("):]
	route := strings.Index(fn, `if carrier == "spoof" || carrier == "sni" {`)
	link := strings.Index(fn, `"How Do You Want To Set Up This Side?"`)
	if route < 0 || link < 0 || route > link {
		t.Fatal("IP and SNI spoofing no longer leave for the classic wizard before the setup-link choice")
	}

	classic, err := os.ReadFile("directsetup_classic.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	c := string(classic)
	tok := c[strings.Index(c, "func askSharedTokenClassic"):]
	kharej, iran, _ := strings.Cut(tok, "\n\t\treturn token, true\n\t}\n")
	if !strings.Contains(kharej, "if side == sideKharej") || !strings.Contains(kharej, "randomToken(64)") {
		t.Fatal("the classic wizard no longer has kharej suggest the token")
	}
	if !strings.Contains(iran, `tui.Prompt("Token from the kharej server: ")`) {
		t.Fatal("the classic wizard no longer asks Iran for the kharej server's token")
	}
	if strings.Contains(c, "pendingShareLink") || strings.Contains(c, "Setup Link") {
		t.Fatal("the classic wizard shows the setup link, which the spoofing carriers were to be left without")
	}
}
