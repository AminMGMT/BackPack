package network

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	mrand "math/rand/v2"
	"strconv"
	"strings"
	"sync"
)

// Which source ports the pck carrier's segments come from, and how the firewall
// rules that protect them are spelled.
//
// This is arithmetic and string building — no sockets, no syscalls, no build
// tag — for the same reason pckframe.go is: the decisions made here are the ones
// worth asserting on in a test, and a test that only runs on Linux is a test
// that mostly does not run. What needs a packet socket lives in
// pckconn_linux.go; what needs iptables lives in pckguard_linux.go.

// pckPortSpan is how many consecutive source ports one client tunnel may use:
// one per carrier, since each KCP session must reach the server as its own peer.
// The pool is configurable and allowed to grow past its configured size, so the
// range is comfortably larger than any pool anyone would run; ports wrap within
// it rather than escaping, so the guard's range always covers what is in use.
const pckPortSpan = 128

// pckPortsInUse records which ports inside the range are held by a live
// carrier, so one is never handed to two.
//
// This was an incrementing counter taken modulo the span, which is correct for
// the first pckPortSpan carriers and wrong for every one after them: the
// counter never came back down, so carrier 128 was given the same port as
// carrier 0 — and carrier 0 is the control channel, which is still on it.
//
// What that does is not subtle, and newPckConn already describes it: kcp-go
// demultiplexes on the sender's address alone, so two carriers on one port
// arrive as one peer, and a packet claiming a new conversation on an existing
// entry closes the old one. The control channel died, the client timed out and
// reconnected, and the operator saw a tunnel that dropped every so often and
// had to be restarted by hand — which worked, because a fresh process starts
// the counter at zero again.
//
// A pool dialling a connection every sixteen seconds, which is what the logs
// from the field showed, walks through the whole span in about half an hour.
var (
	pckPortMu     sync.Mutex
	pckPortsInUse = map[uint16]bool{}
)

// pckClientPortBase is the bottom of the client's source-port range for the
// tunnel with this token.
//
// It is drawn at random when the tunnel opens its first carrier, and kept for as
// long as any of its carriers is open, so a pool shares one range and one set of
// kernel-suppression rules. Once the last carrier closes the range is given up,
// and the next open draws a new one.
//
// It used to be derived from the token, so that a restart would find its old
// rules identical and replace them rather than pile up. That made every restart
// send the same flow — the same source port to the same destination — and that
// is what a middlebox that has stopped passing a flow keeps on dropping.
// Reported on v1.8.4: "a direct pck tunnel stops after about 24 hours, and I
// have to delete it and make it again; auto reset is on both servers." The
// restarts brought back the blocked flow; only a new tunnel, with a new token
// and so a new port, got out. Leftover rules are now found by the tunnel's tag
// instead (see pckTunnelID and sweepTunnelRules), so nothing depends on the
// port staying put.
func pckClientPortBase(token string) uint16 {
	pckPortMu.Lock()
	defer pckPortMu.Unlock()
	id := pckTunnelID(token)
	if b, ok := pckClientBases[id]; ok {
		return b
	}
	for {
		// Into the ephemeral range, which is where a connecting host's port
		// comes from and so where one is expected to be; the span is kept
		// inside it. Two tunnels in one process never share a range.
		b := uint16(32768 + mrand.IntN(28000-pckPortSpan))
		clash := false
		for _, other := range pckClientBases {
			if b < other+pckPortSpan && other < b+pckPortSpan {
				clash = true
			}
		}
		if !clash {
			pckClientBases[id] = b
			return b
		}
	}
}

// pckClientBases is each tunnel's current range, by pckTunnelID.
var pckClientBases = map[string]uint16{}

// pckTunnelID names a tunnel in its firewall rules without naming its token.
func pckTunnelID(token string) string {
	sum := sha256.Sum256([]byte("backpack-pck-v1:" + token))
	return hex.EncodeToString(sum[:4])
}

// legacyPckClientPortBase is the range v1.8.4 and earlier derived from the
// token, still needed once: to clear the rules such a build left behind.
func legacyPckClientPortBase(token string) uint16 {
	sum := sha256.Sum256([]byte("backpack-pck-v1:" + token))
	return 32768 + binary.BigEndian.Uint16(sum[:2])%(28000-pckPortSpan)
}

// nextPckClientPort claims a free source port for one more carrier on a tunnel
// whose range starts at base, or reports that the range is full.
//
// Exhaustion is an error rather than a silent reuse. The span is 128 and a pool
// is a couple of dozen at the outside, so reaching the end means carriers are
// being leaked somewhere — and handing out a port that is already in use is
// exactly the fault this exists to prevent, so it must not be the fallback.
func nextPckClientPort(base uint16) (uint16, error) {
	pckPortMu.Lock()
	defer pckPortMu.Unlock()

	for i := uint16(0); i < pckPortSpan; i++ {
		port := base + i
		if !pckPortsInUse[port] {
			pckPortsInUse[port] = true
			return port, nil
		}
	}
	return 0, fmt.Errorf("pck: all %d source ports from %d are in use", pckPortSpan, base)
}

// releasePckClientPort gives a port back when its carrier closes, and the
// tunnel's range with it once no carrier of the tunnel holds one.
func releasePckClientPort(id string, port uint16) {
	pckPortMu.Lock()
	defer pckPortMu.Unlock()
	delete(pckPortsInUse, port)
	base, ok := pckClientBases[id]
	if !ok {
		return
	}
	for p := base; p < base+pckPortSpan; p++ {
		if pckPortsInUse[p] {
			return
		}
	}
	delete(pckClientBases, id)
}

// portSpec renders a port or a port range in the spelling iptables expects.
// A one-port range is written as the bare port, so the server's rules read the
// way an operator inspecting them would expect.
func portSpec(lo, hi uint16) string {
	if lo == hi {
		return strconv.Itoa(int(lo))
	}
	return strconv.Itoa(int(lo)) + ":" + strconv.Itoa(int(hi))
}

// pckRules is the rule set the guard installs, each entry being the table
// followed by the rule body. They are tagged with a comment naming the tunnel
// (id, from pckTunnelID) and the ports, so a rule left behind by a crash is
// found by the tunnel's tag whatever ports it was for, and is readable by hand.
// An empty id is the untagged spelling v1.8.4 and earlier wrote.
func pckRules(id string, lo, hi uint16) [][]string {
	p := portSpec(lo, hi)
	comment := "backpack-pck-" + p
	if id != "" {
		comment = pckRulePrefix(id) + p
	}
	tag := []string{"-m", "comment", "--comment", comment}

	rule := func(table string, body ...string) []string {
		return append([]string{table}, append(body, tag...)...)
	}
	return [][]string{
		// The one that matters: drop the kernel's replies to segments it thinks
		// arrived at a closed port. Scoped to RSTs leaving from these ports, so
		// nothing else on the machine is affected.
		rule("filter", "OUTPUT", "-p", "tcp", "--sport", p,
			"--tcp-flags", "RST", "RST", "-j", "DROP"),
		// And keep the pseudo-flows out of the connection tracker, in both
		// directions.
		rule("raw", "PREROUTING", "-p", "tcp", "--dport", p, "-j", "NOTRACK"),
		rule("raw", "OUTPUT", "-p", "tcp", "--sport", p, "-j", "NOTRACK"),
	}
}

// scopedPckRules keeps kernel suppression on the carrier's wire path. In
// particular, a backend connection routed over lo must never lose its RST.
func scopedPckRules(id string, lo, hi uint16, iface, local, peer string, peerPort uint16) [][]string {
	rules := pckRules(id, lo, hi)
	for i, r := range rules {
		scope := []string{"-o", iface, "-s", local}
		if r[1] == "PREROUTING" {
			scope = []string{"-i", iface, "-d", local}
		}
		if peer != "" {
			if r[1] == "PREROUTING" {
				scope = append(scope, "-s", peer, "--sport", portSpec(peerPort, peerPort))
			} else {
				scope = append(scope, "-d", peer, "--dport", portSpec(peerPort, peerPort))
			}
		}
		rules[i] = append(append(append([]string{}, r[:4]...), scope...), r[4:]...)
	}
	return rules
}

// pckRulePrefix is how every rule of one tunnel's comment begins.
func pckRulePrefix(id string) string { return "backpack-pck-" + id + "-" }

// pckRuleIDs finds only the tagged format; untagged legacy ranges have no
// reliable tunnel owner and are handled separately during PCK initialization.
func pckRuleIDs(listing string) []string {
	seen := map[string]bool{}
	var ids []string
	for _, del := range tunnelRuleDeletions(listing, "backpack-pck-") {
		for i := 0; i+1 < len(del); i++ {
			if del[i] != "--comment" {
				continue
			}
			tag := strings.TrimPrefix(del[i+1], "backpack-pck-")
			parts := strings.SplitN(tag, "-", 2)
			if len(parts) != 2 || len(parts[0]) != 8 {
				continue
			}
			id := parts[0]
			if _, err := hex.DecodeString(id); err != nil || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// tunnelRuleDeletions turns `iptables -S` output into the delete commands for
// the rules whose comment starts with prefix. Our comments carry no spaces, so
// the listing splits on whitespace exactly as it was written.
func tunnelRuleDeletions(listing, prefix string) [][]string {
	var out [][]string
	for _, line := range strings.Split(listing, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "-A" {
			continue
		}
		mine := false
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "--comment" && strings.HasPrefix(strings.Trim(f[i+1], `"`), prefix) {
				mine = true
			}
		}
		if mine {
			// iptables -S quotes comment values. exec.Command receives arguments
			// directly, so shell quotes must not become part of the comment.
			for i := 0; i+1 < len(f); i++ {
				if f[i] == "--comment" {
					f[i+1] = strings.Trim(f[i+1], `"`)
				}
			}
			out = append(out, append([]string{"-D"}, f[1:]...))
		}
	}
	return out
}
