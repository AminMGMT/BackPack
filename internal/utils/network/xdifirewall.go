package network

import (
	"strings"
)

// Does the firewall let xdi's echoes in?
//
// xdi reads its traffic from a raw ICMP socket, and on Linux a raw socket is
// handed a packet after the INPUT chain has passed it. So a firewall that drops
// ICMP — common on servers in Iran, where answering ping is a way to be found —
// leaves an xdi tunnel that starts, logs nothing wrong, and never completes a
// handshake. pck does not have this problem: its packet socket sees frames
// before the firewall does. Reported from the field as "xdi alone does not work
// at all", on a direct tunnel whose pck twin worked.

// xdiFirewallBlock reads `iptables -S` output and says whether the INPUT path
// would drop the echoes this side reads — echo requests on the server, echo
// replies on the client — and if so, which rule. It is a reading of the rules,
// not a proof: a rule earlier in a chain can accept what a later one drops, and
// this does not follow jumps. It only speaks when it finds a drop that names
// ICMP, or a DROP policy with nothing that accepts ICMP at all.
func xdiFirewallBlock(rules string, server bool) (string, bool) {
	wantType := "echo-reply"
	wantNum := "0"
	if server {
		wantType, wantNum = "echo-request", "8"
	}

	policyDrop := false
	acceptsICMP := false
	for _, line := range strings.Split(rules, "\n") {
		line = strings.TrimSpace(line)
		if line == "-P INPUT DROP" {
			policyDrop = true
			continue
		}
		if !strings.HasPrefix(line, "-A INPUT") {
			continue
		}
		f := strings.Fields(line)
		proto, icmpType, target := "", "", ""
		for i := 0; i+1 < len(f); i++ {
			switch f[i] {
			case "-p", "--protocol":
				proto = f[i+1]
			case "--icmp-type":
				icmpType = f[i+1]
			case "-j", "--jump":
				target = f[i+1]
			}
		}
		if proto != "icmp" && proto != "1" {
			continue
		}
		typeMatches := icmpType == "" || icmpType == wantType || icmpType == wantNum ||
			strings.HasPrefix(icmpType, wantNum+"/")
		if !typeMatches {
			continue
		}
		switch target {
		case "ACCEPT":
			acceptsICMP = true
		case "DROP", "REJECT":
			if !acceptsICMP {
				return line, true
			}
		}
	}
	if policyDrop && !acceptsICMP {
		return "-P INPUT DROP (and no rule accepting ICMP " + wantType + ")", true
	}
	return "", false
}
