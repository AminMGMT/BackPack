package health

import "testing"

// Reading `iptables -S INPUT` for whether it would drop GRE.
func TestGREFirewallBlock(t *testing.T) {
	cases := []struct {
		name, rules string
		blocked     bool
	}{
		{"an open firewall", "-P INPUT ACCEPT\n", false},
		{"a drop policy with nothing for GRE", "-P INPUT DROP\n-A INPUT -p tcp -m tcp --dport 22 -j ACCEPT\n", true},
		{"a drop policy with the tunnel's own rule",
			"-P INPUT DROP\n-A INPUT -s 203.0.113.9/32 -p gre -m comment --comment \"backpack-gre bp1\" -j ACCEPT\n", false},
		{"a drop policy with established traffic let in",
			"-P INPUT DROP\n-A INPUT -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT\n", false},
		{"loopback alone is not GRE", "-P INPUT DROP\n-A INPUT -i lo -j ACCEPT\n", true},
		{"an explicit drop of protocol 47", "-P INPUT ACCEPT\n-A INPUT -p 47 -j DROP\n", true},
		{"a drop after an accept", "-P INPUT ACCEPT\n-A INPUT -p gre -j ACCEPT\n-A INPUT -p gre -j DROP\n", false},
	}
	for _, tc := range cases {
		if _, got := greFirewallBlock(tc.rules); got != tc.blocked {
			t.Errorf("%s: blocked = %v, want %v", tc.name, got, tc.blocked)
		}
	}
}
