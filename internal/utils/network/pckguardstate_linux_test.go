//go:build linux

package network

import "testing"

// The guard is judged from `iptables -S`: a rule counts for a tunnel when its
// comment starts with that tunnel's prefix, and for no other.
func TestCountPckRulesCountsOnlyTheTunnelsOwn(t *testing.T) {
	mine, other := pckTunnelID("a-token"), pckTunnelID("another")
	listing := "-P OUTPUT ACCEPT\n" +
		"-A OUTPUT -o eth0 -p tcp -m tcp --sport 40000:40127 --tcp-flags RST RST -m comment --comment backpack-pck-" + mine + "-40000:40127 -j DROP\n" +
		"-A OUTPUT -p tcp --sport 22 -j ACCEPT\n" +
		"-A PREROUTING -i eth0 -p tcp --dport 40000:40127 -m comment --comment \"backpack-pck-" + mine + "-40000:40127\" -j NOTRACK\n" +
		"-A OUTPUT -p tcp --sport 9000 -m comment --comment backpack-pck-" + other + "-9000 -j NOTRACK\n"
	if got := countPckRules(listing, pckRulePrefix(mine)); got != 2 {
		t.Fatalf("counted %d of this tunnel's rules, want 2", got)
	}
	if got := countPckRules(listing, pckRulePrefix(pckTunnelID("nobody"))); got != 0 {
		t.Fatalf("counted %d rules for a tunnel with none", got)
	}
}
