package network

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"
	"sync/atomic"
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

// pckPortSeq hands out the offset within that range. One tunnel runs per
// process, so a package-level counter is exactly the right scope.
var pckPortSeq atomic.Uint32

// pckClientPortBase derives the bottom of the client's source-port range from
// the tunnel token.
//
// Deriving the range rather than picking it at random is what keeps the
// kernel-suppression rules to one set for the tunnel's life. The rules are
// written against the port range, so a range that changed on every reconnect
// would leave a new set behind each time — and on a flaky link, where reconnects
// are the whole point of this transport, they would accumulate until something
// noticed. It also means a middlebox watching the pair sees the same flows
// resume rather than a new set appear.
//
// What is NOT derived is which port within the range a given carrier takes: see
// newPckConn for why they must differ.
func pckClientPortBase(token string) uint16 {
	sum := sha256.Sum256([]byte("backpack-pck-v1:" + token))
	// Into the ephemeral range, which is where a connecting host's port comes
	// from and so where one is expected to be. The span is subtracted so the top
	// of the range cannot run past the end of it.
	return 32768 + binary.BigEndian.Uint16(sum[:2])%(28000-pckPortSpan)
}

// nextPckClientPort returns the source port for one more carrier on a tunnel
// whose range starts at base.
func nextPckClientPort(base uint16) uint16 {
	return base + uint16(pckPortSeq.Add(1)-1)%pckPortSpan
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
// followed by the rule body. They are tagged with a comment naming the ports, so
// a rule left behind by a crash is identifiable and removable by hand.
func pckRules(lo, hi uint16) [][]string {
	p := portSpec(lo, hi)
	tag := []string{"-m", "comment", "--comment", fmt.Sprintf("backpack-pck-%s", p)}

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
