//go:build linux

package network

import (
	"fmt"
	"os/exec"
	"strconv"
)

// Keeping the host kernel out of a conversation it is not part of.
//
// The pck carrier's segments are addressed to a port nothing on the machine is
// listening on, because the listener is this process reading the wire directly.
// The kernel does not know that. It sees a TCP segment for a closed port and
// does the correct thing for a host that has no such service: it answers with a
// RST. That RST goes to the peer, and any stateful device between the two —
// which on these routes is most of them — takes it as the connection ending and
// stops passing the flow. The tunnel then dies for a reason that appears
// nowhere, having worked for a few seconds.
//
// Connection tracking is the smaller half of the same problem. Every one of
// these pseudo-flows would get a conntrack entry it will never need, and on a
// busy server that is a table filling up for nothing.
//
// Both are fixed by two narrow rules, installed for the tunnel's port only and
// removed when it closes. paqet, which has the same problem, documents them and
// leaves them to the operator; there is no reason a tunnel that already knows
// its own port cannot install them itself.
type pckGuard struct {
	port  uint16
	rules [][]string // each entry is a full rule body, table first
	added [][]string
}

// installPckGuard adds the rules for a port and returns a handle that remembers
// which of them took, so remove undoes exactly that much.
//
// Best effort throughout: a machine without iptables still runs the tunnel, and
// the carrier says so at startup rather than failing. What it must not do is
// report success for a rule that was refused, which is why each is checked.
func installPckGuard(port uint16) *pckGuard {
	g := &pckGuard{port: port, rules: pckRules(port)}
	if _, err := exec.LookPath("iptables"); err != nil {
		return g
	}
	for _, r := range g.rules {
		table, body := r[0], r[1:]
		args := append([]string{"-t", table, "-I"}, body...)
		if err := exec.Command("iptables", args...).Run(); err == nil {
			g.added = append(g.added, r)
		}
	}
	return g
}

// remove deletes the rules that were installed. Safe on a nil guard and on one
// that installed nothing.
func (g *pckGuard) remove() {
	if g == nil {
		return
	}
	for _, r := range g.added {
		table, body := r[0], r[1:]
		args := append([]string{"-t", table, "-D"}, body...)
		_ = exec.Command("iptables", args...).Run()
	}
	g.added = nil
}

// Installed reports whether every rule is in place, so the carrier can warn
// when the tunnel is running without the protection.
func (g *pckGuard) Installed() bool {
	return g != nil && len(g.added) == len(g.rules)
}

// pckRules is the rule set, each entry being the table followed by the rule
// body. They are tagged with a comment naming the port, so a rule left behind
// by a crash is identifiable and removable by hand.
func pckRules(port uint16) [][]string {
	p := strconv.Itoa(int(port))
	tag := []string{"-m", "comment", "--comment", fmt.Sprintf("backpack-pck-%s", p)}

	rule := func(table string, body ...string) []string {
		return append([]string{table}, append(body, tag...)...)
	}
	return [][]string{
		// The one that matters: drop the kernel's replies to segments it thinks
		// arrived at a closed port. Scoped to RSTs leaving from this port, so
		// nothing else on the machine is affected.
		rule("filter", "OUTPUT", "-p", "tcp", "--sport", p,
			"--tcp-flags", "RST", "RST", "-j", "DROP"),
		// And keep the pseudo-flows out of the connection tracker, in both
		// directions.
		rule("raw", "PREROUTING", "-p", "tcp", "--dport", p, "-j", "NOTRACK"),
		rule("raw", "OUTPUT", "-p", "tcp", "--sport", p, "-j", "NOTRACK"),
	}
}
