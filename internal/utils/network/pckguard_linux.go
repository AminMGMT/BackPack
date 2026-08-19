//go:build linux

package network

import (
	"os/exec"
	"strconv"
	"sync"
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
// Both are fixed by three narrow rules, installed for the tunnel's ports only
// and removed when the last carrier using them closes. paqet, which has the same
// problem, documents them and leaves them to the operator; there is no reason a
// tunnel that already knows its own ports cannot install them itself.
//
// The rules cover a port RANGE rather than a single port, because a client's
// pool opens one carrier per session and each needs its own source port (see
// newPckConn). They are refcounted so that a pool of sixteen installs one set of
// rules rather than sixteen.
type pckGuard struct {
	lo, hi uint16
	rules  [][]string // each entry is a full rule body, table first
	added  [][]string
}

// Guards are shared per port range within a process, so the pool's carriers all
// reference one rule set and the last one out removes it.
var (
	guardMu     sync.Mutex
	guardShared = map[string]*sharedGuard{}
)

type sharedGuard struct {
	g   *pckGuard
	ref int
}

func guardKey(lo, hi uint16) string {
	return strconv.Itoa(int(lo)) + ":" + strconv.Itoa(int(hi))
}

// installPckGuard adds the rules for a port range and returns a handle that
// remembers which of them took, so remove undoes exactly that much. Calling it
// again for the same range takes a reference on the rules already installed.
//
// Best effort throughout: a machine without iptables still runs the tunnel, and
// the carrier says so at startup rather than failing. What it must not do is
// report success for a rule that was refused, which is why each is checked.
func installPckGuard(lo, hi uint16) *pckGuard {
	guardMu.Lock()
	defer guardMu.Unlock()

	key := guardKey(lo, hi)
	if sh, ok := guardShared[key]; ok {
		sh.ref++
		return sh.g
	}

	g := &pckGuard{lo: lo, hi: hi, rules: pckRules(lo, hi)}
	if _, err := exec.LookPath("iptables"); err != nil {
		guardShared[key] = &sharedGuard{g: g, ref: 1}
		return g
	}
	for _, r := range g.rules {
		table, body := r[0], r[1:]
		// Anything identical still in the table is ours, left behind by a
		// process that did not exit cleanly. See sweepPckRule.
		sweepPckRule(table, body)
		args := append([]string{"-t", table, "-I"}, body...)
		if err := exec.Command("iptables", args...).Run(); err == nil {
			g.added = append(g.added, r)
		}
	}
	guardShared[key] = &sharedGuard{g: g, ref: 1}
	return g
}

// sweepPckRule deletes every copy of one rule before it is installed again.
//
// The rules are only removed when a carrier closes cleanly. A crash, a kill, a
// `systemctl restart` or a failed start leaves them in the table — and because
// the port is derived from the tunnel's token rather than drawn at random, the
// next start asks for the *identical* rule. iptables is happy to hold a
// thousand copies of the same line, so without this they accumulate for as
// long as the tunnel is ever restarted, and every packet is matched against
// all of them.
//
// One tunnel that had been restarted through an afternoon of configuration was
// found with several hundred copies of a single rule. Nothing warns about it:
// the tunnel works, the firewall just gets slower and less readable forever.
//
// Deleting first and adding once leaves exactly one, and clears whatever an
// earlier run left behind. The cap is there so that a delete which reports
// success without removing anything cannot spin.
func sweepPckRule(table string, body []string) {
	args := append([]string{"-t", table, "-D"}, body...)
	for i := 0; i < 1024; i++ {
		if err := exec.Command("iptables", args...).Run(); err != nil {
			return // no more copies, which is the ordinary first-start case
		}
	}
}

// remove drops this carrier's reference and deletes the rules once the last one
// is gone. Safe on a nil guard and on one that installed nothing.
func (g *pckGuard) remove() {
	if g == nil {
		return
	}
	guardMu.Lock()
	defer guardMu.Unlock()

	key := guardKey(g.lo, g.hi)
	sh, ok := guardShared[key]
	if !ok {
		return // already torn down
	}
	if sh.ref--; sh.ref > 0 {
		return // another carrier is still using the rules
	}
	delete(guardShared, key)

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

// portSpec and pckRules — the spelling of the rules themselves — live in
// pckport.go, which carries no build tag so they can be asserted on anywhere.
