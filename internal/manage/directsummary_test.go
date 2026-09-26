package manage

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// capture runs f and returns everything it printed to stdout.
func capture(t *testing.T, f func()) string {
	t.Helper()
	saved := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	f()
	w.Close()
	os.Stdout = saved
	return <-done
}

// The token has to be on screen when the operator is told to use it on the
// other machine. It was not: the reminder took the token as an argument and
// never printed it, leaving "use the same token" with no token in sight.
func TestReminderShowsTheToken(t *testing.T) {
	const token = "a-very-distinctive-token-value-9f3a2b"

	for _, side := range []directSide{sideIran, sideKharej} {
		out := capture(t, func() { remindOtherSide(side, token) })
		if !strings.Contains(out, token) {
			t.Fatalf("the %s reminder did not print the token:\n%s", side, out)
		}
		// And it must say which machine to go to next.
		want := "KHAREJ"
		if side == sideKharej {
			want = "IRAN"
		}
		if !strings.Contains(out, want) {
			t.Fatalf("the %s reminder did not name the next machine:\n%s", side, out)
		}
	}
}

// The direct wizard's own summary went with the wizard: a direct tunnel is
// built on the layer-3 engine now, so summariseL3 is what runs before every
// one of them. What was here checked a function nothing called.

// The layer-3 summary is one short screen: the carrier in the menu's words,
// one fact a line, and on Iran the setup link the kharej pastes.
func TestL3SummaryShowsWhatWasAsked(t *testing.T) {
	cfg := l3Spec{
		Name: "demo", Side: sideIran, Carrier: "pck", Encap: "gre", GREKey: 42,
		Addr: "203.0.113.9:9000", Token: "the-token", Ports: []string{"3233"},
		Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
		Preset: PresetBalance,
	}
	link := pendingShareLink(cfg)
	if !strings.HasPrefix(link, shareScheme) {
		t.Fatalf("no setup link could be built before the tunnel exists: %q", link)
	}
	out := capture(t, func() { summariseL3(cfg, link) })

	for _, want := range []string{
		"Direct PCK", "bp0", "10.10.0.1/30", "10.10.0.2",
		"203.0.113.9:9000", "3233", "GRE Key", "42", "Balance",
		"Setup Link (Setup Kharej → Direct → The Same Carrier → Setup Link)", link,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the layer-3 summary is missing %q:\n%s", want, out)
		}
	}

	// The link shown before the file exists is the one the kharej can use:
	// it decodes, and mirrors into this tunnel's other end.
	parsed, err := DecodeShareLink(link)
	if err != nil {
		t.Fatalf("the summary's link does not decode: %v", err)
	}
	form := MirrorForPeer(parsed)
	if form.Token != "the-token" || hostOnly(form.LocalIP) != "10.10.0.2" || form.PeerIP != "10.10.0.1" ||
		form.TunnelPort != "9000" || form.GREKey != 42 {
		t.Fatalf("the summary's link builds the wrong kharej: %+v", form)
	}

	// A udp tunnel over several sockets names the kharej ports it needs open.
	udp := cfg
	udp.Carrier, udp.Paths = "udp", 4
	if out := capture(t, func() { summariseL3(udp, "") }); !strings.Contains(out, "Direct UDP") ||
		!strings.Contains(out, "9000-9003") {
		t.Fatalf("a four-socket udp tunnel does not show its port range:\n%s", out)
	}

	// No ports is a plain TUN, and says so.
	cfg.Ports = nil
	if out := capture(t, func() { summariseL3(cfg, "") }); !strings.Contains(out, "none (TUN)") {
		t.Fatalf("a tunnel without ports is not shown as TUN:\n%s", out)
	}
}
