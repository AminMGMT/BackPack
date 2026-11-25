package manage

import (
	"slices"
	"testing"
)

// The reverse xDi is on the menu, in both the terminal and the panel, as
// xDiRvs — transport = "xdi", the engine's own name for it.
func TestXdiRvsIsAReverseTransport(t *testing.T) {
	found := false
	for _, g := range cliTransportGroups() {
		for _, e := range g.entries {
			if e.label == "xDiRvs" {
				found = true
				if e.value != "xdi" {
					t.Fatalf("xDiRvs sets transport = %q, want xdi", e.value)
				}
			}
		}
	}
	if !found {
		t.Fatal("xDiRvs is not on the reverse transport menu")
	}
	if !validTransport("xdi") {
		t.Fatal("the reverse engine does not take xdi")
	}
	if got := transportLabel("xdi"); got != "xDiRvs" {
		t.Fatalf("an xdi tunnel is shown as %q, want xDiRvs", got)
	}
}

// The Connection Test tries the plain and websocket transports and xDiRvs
// for real, each measured for speed. xDiRvs needs root on both servers, as
// pck does, and its row is named as the menu names it, apart from the direct
// carrier's.
func TestTheConnectionTestCoversThePlainTransportsAndXdiRvs(t *testing.T) {
	for _, tr := range []string{"tcp", "tcpmux", "ws", "wsmux", "wss", "wssmux", "xdi"} {
		if !slices.Contains(connTestReverse, tr) {
			t.Errorf("the connection test does not try reverse %s", tr)
		}
	}
	if !connTestNeedsRoot("xdi") {
		t.Error("reverse xdi is tried without root, which its ICMP socket needs")
	}
	if got := ctNameOf("reverse", "xdi"); got != "xDiRvs" {
		t.Errorf("the reverse xdi row is called %q, want xDiRvs", got)
	}
	if got := ctNameOf("direct", "xdi"); got != "xDi" {
		t.Errorf("the direct xdi row is called %q, want xDi", got)
	}
}
