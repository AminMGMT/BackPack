package network

import "testing"

func TestTheXdiFirewallCheck(t *testing.T) {
	cases := []struct {
		name   string
		rules  string
		server bool
		block  bool
	}{
		{"open", "-P INPUT ACCEPT\n-A INPUT -p tcp --dport 22 -j ACCEPT", true, false},
		{"all icmp dropped", "-P INPUT ACCEPT\n-A INPUT -p icmp -j DROP", true, true},
		{"echo requests dropped, server", "-A INPUT -p icmp -m icmp --icmp-type 8 -j DROP", true, true},
		{"echo requests dropped, client", "-A INPUT -p icmp -m icmp --icmp-type 8 -j DROP", false, false},
		{"echo replies rejected, client", "-A INPUT -p icmp -m icmp --icmp-type echo-reply -j REJECT", false, true},
		{"accepted before dropped", "-A INPUT -p icmp -j ACCEPT\n-A INPUT -p icmp -j DROP", true, false},
		{"drop policy, nothing for icmp", "-P INPUT DROP\n-A INPUT -p tcp --dport 22 -j ACCEPT", true, true},
		{"drop policy, icmp accepted", "-P INPUT DROP\n-A INPUT -p icmp -j ACCEPT", true, false},
		{"other chain only", "-A FORWARD -p icmp -j DROP", true, false},
	}
	for _, c := range cases {
		_, got := xdiFirewallBlock(c.rules, c.server)
		if got != c.block {
			t.Errorf("%s: block = %v, want %v", c.name, got, c.block)
		}
	}
}
