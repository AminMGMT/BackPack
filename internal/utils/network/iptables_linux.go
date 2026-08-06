//go:build linux

package network

import (
	"fmt"
	"os/exec"
	"strconv"
)

// The tcp spoof profile forges TCP segments to a port the host is not listening
// on. The kernel answers each with a RST addressed to the (forged) source, which
// would tear the flow down before KCP ever sees it. rstGuard installs a single,
// targeted iptables rule that drops exactly those kernel RSTs — outbound TCP
// segments with the RST flag whose source port is the tunnel's — and removes it
// again when the tunnel closes.
//
// It is deliberately narrow: it never touches the inbound packets (our raw
// socket still reads them), and it matches only RSTs on the one port, so no
// other TCP traffic is affected. It is best effort — if iptables is missing or
// the rule cannot be installed, the tunnel still runs; the operator is warned
// that the kernel's RSTs may disrupt it.
type rstGuard struct {
	port   uint16
	rule   []string
	active bool
}

// installRSTGuard adds the RST-drop rule for a port and returns a handle that
// remembers whether it took, so remove() only undoes what was actually added.
func installRSTGuard(port uint16) *rstGuard {
	g := &rstGuard{port: port, rule: rstRule(port)}
	if _, err := exec.LookPath("iptables"); err != nil {
		// No iptables: nothing to install. The carrier logs the consequence.
		return g
	}
	// -I inserts at the top so the DROP wins over any accept rule below it.
	args := append([]string{"-I"}, g.rule...)
	if err := exec.Command("iptables", args...).Run(); err == nil {
		g.active = true
	}
	return g
}

// remove deletes the rule if it was installed. Safe to call on a guard that
// never installed anything.
func (g *rstGuard) remove() {
	if g == nil || !g.active {
		return
	}
	args := append([]string{"-D"}, g.rule...)
	_ = exec.Command("iptables", args...).Run()
	g.active = false
}

// Installed reports whether the rule is in place, so the carrier can warn when
// it is not.
func (g *rstGuard) Installed() bool { return g != nil && g.active }

// rstRule is the iptables rule body (everything after -I/-D), matching outbound
// RST segments on the tunnel's source port, tagged with a comment so a leftover
// rule from a crash is identifiable.
func rstRule(port uint16) []string {
	p := strconv.Itoa(int(port))
	return []string{
		"OUTPUT",
		"-p", "tcp",
		"--sport", p,
		"--tcp-flags", "RST", "RST",
		"-m", "comment", "--comment", fmt.Sprintf("backpack-spoof-%s", p),
		"-j", "DROP",
	}
}
