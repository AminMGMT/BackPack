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

// The summary is what the operator confirms against, so everything they were
// asked for has to be in it.
func TestSummaryShowsWhatWasAsked(t *testing.T) {
	cfg := directSpec{
		Name: "demo", Side: sideIran, Transport: "stealth",
		Addr: "203.0.113.9:8443", Token: "the-token",
		Ports: []string{"443", "8080=80"}, AcceptUDP: true,
		MaxConnections: 50, BandwidthMbps: 200, Sessions: 4, Preset: PresetThroughput,
	}
	out := capture(t, func() { summariseDirect(cfg) })

	for _, want := range []string{
		"203.0.113.9:8443", // where it dials
		"443",              // the ports
		"stealth",          // the transport
		"the-token",        // the token, for the other machine
		"50 connections",   // the caps, in words
		"200 Mbit/s",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the summary is missing %q:\n%s", want, out)
		}
	}
}

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
