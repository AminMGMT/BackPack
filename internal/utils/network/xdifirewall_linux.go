//go:build linux

package network

import (
	"os/exec"
)

// XdiFirewallNote reads this host's iptables rules and returns a warning when
// they would keep xdi's echoes from ever reaching it, or "" when they would not
// or cannot be read.
func XdiFirewallNote(server bool) string {
	out, err := exec.Command("iptables", "-S", "INPUT").Output()
	if err != nil {
		return ""
	}
	rule, blocked := xdiFirewallBlock(string(out), server)
	if !blocked {
		return ""
	}
	what := "echo replies from the server"
	if server {
		what = "echo requests from the client"
	}
	return "xdi: this server's firewall looks like it drops " + what + " (" + rule + "). " +
		"xdi reads them after the firewall, so the tunnel cannot come up until ICMP is let in — " +
		"for example: iptables -I INPUT -p icmp -j ACCEPT"
}

// FirewallNote is the warning for this carrier: nothing when its own accept
// rule is in place, since that lets its echoes through whatever the rest of
// the firewall drops; otherwise what XdiFirewallNote reads off the rules.
func (c *icmpConn) FirewallNote() string {
	if c.accept.Installed() {
		return ""
	}
	return XdiFirewallNote(c.server)
}
