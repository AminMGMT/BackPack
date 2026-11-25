package manage

import "testing"

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
