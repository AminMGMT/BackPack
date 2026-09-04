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

// The layer-3 summary has its own set, including the ping that proves it.
func TestL3SummaryShowsWhatWasAsked(t *testing.T) {
	cfg := l3Spec{
		Name: "demo", Side: sideIran, Carrier: "pck", Encap: "gre", GREKey: 42,
		Addr: "203.0.113.9:9000", Token: "the-token",
		Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
	}
	out := capture(t, func() { summariseL3(cfg) })

	for _, want := range []string{
		"pck", "gre (key 42)", "bp0", "10.10.0.1/30", "10.10.0.2",
		"1400", "the-token",
		"ping 10.10.0.2", // how to check it worked
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the layer-3 summary is missing %q:\n%s", want, out)
		}
	}
}
