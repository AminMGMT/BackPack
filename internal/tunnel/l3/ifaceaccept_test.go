package l3

import (
	"strings"
	"testing"
)

// The rule lets in what arrives on the tunnel's own interface and nothing
// else, and is tagged with that interface, so two tunnels each hold their own.
func TestTheInterfaceRuleNamesOnlyItsInterface(t *testing.T) {
	got := strings.Join(ifaceAcceptRule("bp3"), " ")
	if got != "INPUT -i bp3 -m comment --comment backpack-l3 bp3 -j ACCEPT" {
		t.Errorf("rule = %q", got)
	}
	if ifaceAcceptComment("bp3") == ifaceAcceptComment("bp4") {
		t.Error("two interfaces share a tag; one stopping would take the other's rule")
	}
}
