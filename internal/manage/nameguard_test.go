package manage

import (
	"strings"
	"testing"
)

// A tunnel name becomes a path by concatenation — app.ConfigPath is
// ConfigDir + "/" + name + ".toml" — so a name carrying a separator names a
// file somewhere else entirely.
//
// Nothing local produces one: the wizard and the panel's create form both check
// the name on the way in. What reaches these two readers unchecked is a name off
// the wire — the ?name= on /api/tunnel/settings, and the body of a node's
// OpSettings request — and until this guard existed there was nothing between
// either of them and the path.
//
// Both callers already hold full administrative authority, so this was never an
// escalation. It is checked because the check exists, costs one line, and its
// absence is the kind of thing that becomes an escalation the first time a
// lower-privileged caller is added.
func TestATunnelNameThatIsAPathIsRefusedBeforeItBecomesOne(t *testing.T) {
	bad := []string{
		"../../../etc/passwd",
		"../secret",
		"a/b",
		`a\b`,
		"",
		"  ",
		"with space",
		strings.Repeat("x", 41),
		"quote\"name",
		"<script>",
	}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadSpec(name); err == nil {
				t.Fatalf("LoadSpec(%q) was accepted", name)
			} else if !strings.Contains(err.Error(), "not a valid tunnel name") {
				t.Fatalf("LoadSpec(%q) failed for the wrong reason: %v", name, err)
			}
			if _, err := LoadTunnelConfig(name); err == nil {
				t.Fatalf("LoadTunnelConfig(%q) was accepted", name)
			} else if !strings.Contains(err.Error(), "not a valid tunnel name") {
				t.Fatalf("LoadTunnelConfig(%q) failed for the wrong reason: %v", name, err)
			}
		})
	}
}

// And an ordinary name still gets the ordinary answer: the guard must not turn
// "there is no tunnel called that" into "that is not a name", which would be a
// worse message for the far commoner case.
func TestAnOrdinaryNameStillReachesTheFilesystem(t *testing.T) {
	_, err := LoadSpec("a-tunnel_that.does-not-exist")
	if err == nil {
		t.Skip("a tunnel by that name exists on this machine")
	}
	if strings.Contains(err.Error(), "not a valid tunnel name") {
		t.Fatalf("a valid name was refused by the guard: %v", err)
	}
}
