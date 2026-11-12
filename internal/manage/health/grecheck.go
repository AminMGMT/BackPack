package health

import (
	"os"
	"os/exec"
	"strings"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tunnel/l3"
)

// The checks only a kernel GRE tunnel has.
//
// A GRE link is up as soon as it is created, whether or not anything reaches
// it, so the two ways it goes quiet without an error are both outside the
// engine: the kernel module that provides the link type, and a firewall that
// drops IP protocol 47 before the link sees it. The engine lets its own GRE in
// with one rule while it runs; these say what is in the way when it cannot.

// greModuleName is the kernel module a GRE carrier's link type comes from.
func greModuleName(carrier string) string {
	if strings.EqualFold(strings.TrimSpace(carrier), l3.CarrierIP6GRE) {
		return "ip6_gre"
	}
	return "ip_gre"
}

// greChecks are the GRE tunnel's own lines on the health screen.
func greChecks(g string, l config.L3Config) []Check {
	out := []Check{{Group: g, Name: "Encryption", Level: CheckInfo,
		Detail: "none — kernel GRE is bare IP protocol 47; what crosses it is in the clear unless it is encrypted itself"}}

	module := greModuleName(l.Carrier)
	if _, err := os.Stat("/sys/module/" + module); err == nil || ifaceExists(ifaceOrDefault(l.Iface)) {
		out = append(out, Check{Group: g, Name: "GRE module", Level: CheckOK, Detail: module + " loaded"})
	} else {
		out = append(out, Check{Group: g, Name: "GRE module", Level: CheckFail,
			Detail: module + " is not loaded, so the kernel cannot create the link",
			Fix:    "modprobe " + module + " ; echo " + module + " > /etc/modules-load.d/backpack-gre.conf"})
	}

	tool := "iptables"
	if strings.EqualFold(strings.TrimSpace(l.Carrier), l3.CarrierIP6GRE) {
		tool = "ip6tables"
	}
	rules, err := exec.Command(tool, "-S", "INPUT").Output()
	if err != nil {
		return out
	}
	if rule, blocked := greFirewallBlock(string(rules)); blocked {
		out = append(out, Check{Group: g, Name: "Firewall", Level: CheckWarn,
			Detail: "this server's firewall looks like it drops IP protocol 47 (" + rule + "), and the tunnel's own rule is not in place",
			Fix:    tool + " -I INPUT -p 47 -s " + strings.Trim(l.Addr, "[]") + " -j ACCEPT"})
	} else {
		out = append(out, Check{Group: g, Name: "Firewall", Level: CheckOK, Detail: "lets IP protocol 47 in"})
	}
	return out
}

// ifaceOrDefault is an interface name with the engine's default filled in.
func ifaceOrDefault(iface string) string {
	if iface == "" {
		return "bp0"
	}
	return iface
}

// greFirewallBlock reads `iptables -S INPUT` and says whether it would drop
// GRE: a rule that drops protocol 47 before anything accepts it, or a DROP
// policy with nothing that accepts it at all. Like xdi's reading of the same
// output it does not follow jumps, so it speaks only when it finds one of
// those two.
func greFirewallBlock(rules string) (string, bool) {
	policyDrop, accepts := false, false
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
		proto, target, other := "", "", false
		for i := 0; i+1 < len(f); i++ {
			switch f[i] {
			case "-p", "--protocol":
				proto = f[i+1]
			case "-j", "--jump":
				target = f[i+1]
			case "-m", "-i", "--in-interface", "--dport", "--sport":
				// A rule narrowed by anything this does not read says
				// nothing either way about GRE. Connection state is read: the
				// two ends ping each other, so the other end's GRE is a
				// reply the kernel tracks, and an ESTABLISHED rule lets it in.
				if f[i] != "-m" || (f[i+1] != "comment" && f[i+1] != "conntrack" && f[i+1] != "state") {
					other = true
				}
			}
		}
		gre := proto == "47" || proto == "gre"
		switch {
		case target == "ACCEPT" && (gre || (proto == "" && !other && !strings.Contains(line, "-s "))):
			accepts = true
		case (target == "DROP" || target == "REJECT") && gre && !accepts:
			return line, true
		}
	}
	if policyDrop && !accepts {
		return "-P INPUT DROP, and no rule accepting protocol 47", true
	}
	return "", false
}
