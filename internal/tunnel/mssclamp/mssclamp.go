// Package mssclamp caps the TCP segment size of connections crossing a tunnel
// interface.
//
// This is the fix for the one failure a tunnel interface produces that looks
// like nothing at all: ping works, SSH works, a small page loads — and every
// large transfer stalls forever.
//
// # The fault
//
// A tunnel's MTU is smaller than the MTU of the interfaces at either end,
// because the encapsulation takes its cut. A TCP connection crossing the tunnel
// does not know that. Its two endpoints negotiate a segment size from *their*
// interfaces — 1460 off an ordinary 1500-byte link — and then send full-sized
// segments that cannot fit.
//
// The kernel is supposed to learn this from an ICMP "fragmentation needed"
// message and lower its estimate. Path MTU discovery is exactly what that is
// for. But those messages are dropped by a great many networks, and dropped
// with particular enthusiasm on the routes these tunnels exist to cross. So
// nothing learns. Small packets keep arriving, which is why every liveness
// check passes, and the first full-sized segment of every download disappears.
//
// # The fix
//
// Rewrite the MSS option in the SYN of every TCP connection leaving the tunnel
// interface, so both endpoints agree on a segment size that fits before a
// single byte of data is sent. Nothing has to be discovered, so nothing depends
// on an ICMP message arriving.
//
// Both chains are needed and they cover different traffic. FORWARD is for
// packets routed *through* this host — the private-network case, where the
// tunnel joins two networks. OUTPUT is for connections that start on this host,
// which is what a forwarded port does. A rule on one chain alone leaves the
// other kind broken in exactly the way described above.
//
// # Why this is shared rather than copied
//
// Two tunnel kinds create interfaces and both need this. An earlier version of
// this code existed once per kind, which is how the pck carrier came to have
// five hundred copies of one firewall rule on a machine in the field: the add
// path and the remove path drifted. The sweep below is the part that must not
// drift, so there is one of it.
package mssclamp

import "strconv"

// Off disables clamping entirely. Zero means automatic, so a distinct value is
// needed for "no, really, leave it alone".
const Off = -1

// Header sizes subtracted from the interface MTU to get the largest segment
// that fits. TCP options are not subtracted: they live inside the segment, and
// the MSS is defined as the payload beneath them.
const (
	OverheadV4 = 40 // 20 IPv4 + 20 TCP
	OverheadV6 = 60 // 40 IPv6 + 20 TCP
)

// For works out the value to clamp to. configured is the operator's setting:
// zero derives it from the MTU, which is what almost every tunnel wants.
func For(configured, mtu, overhead int) int {
	if configured > 0 {
		return configured
	}
	return mtu - overhead
}

// Rule is one firewall rule to install.
//
// The verb (-A, -D, -C) is deliberately not part of it, and neither is the
// chain kept in the same slice. An earlier version stored the whole command as
// one list and overwrote index 2 with the verb — but index 2 was the chain, so
// every command came out as "iptables -t mangle -A -o bp0 ..." with no chain at
// all. iptables refused all of them, the refusal was logged at debug level, and
// the clamp silently did not exist on any machine it was supposed to protect.
//
// Args builds the command instead, so the verb has a slot of its own and cannot
// displace anything.
type Rule struct {
	Cmd   string // iptables or ip6tables
	Table string // mangle
	Chain string // FORWARD or OUTPUT
	Label string
	MSS   int

	// Spec is the match and target, without table, verb or chain.
	Spec []string
}

// Args renders the full command line for one verb: -A to add, -D to delete,
// -C to test.
func (r Rule) Args(verb string) []string {
	args := make([]string, 0, 4+len(r.Spec))
	args = append(args, "-t", r.Table, verb, r.Chain)
	return append(args, r.Spec...)
}

// Rules is every rule the clamp consists of for one interface: two chains for
// each of the two address families.
//
// kind names the tunnel that owns them ("l3", "gre") and appears in the rule
// comment, so two kinds on one host cannot sweep each other's rules away and an
// operator reading `iptables -S` can see which tunnel put them there.
//
// IPv6 is included because one tunnel carries both — the first nibble of the
// inner packet decides which, so an IPv6 flow crosses the same interface and
// hits the same MTU.
func Rules(kind, iface string, mtu, configured int) []Rule {
	var out []Rule
	for _, f := range []struct {
		cmd      string
		label    string
		overhead int
	}{
		{"iptables", "IPv4", OverheadV4},
		{"ip6tables", "IPv6", OverheadV6},
	} {
		mss := For(configured, mtu, f.overhead)
		if mss <= 0 {
			continue
		}
		for _, chain := range []string{"FORWARD", "OUTPUT"} {
			out = append(out, Rule{
				Cmd:   f.cmd,
				Table: "mangle",
				Chain: chain,
				Label: f.label,
				MSS:   mss,
				Spec:  spec(kind, iface, mss),
			})
		}
	}
	return out
}

// spec is the match and target. The comment is what makes a rule findable
// later, by the sweep below and by an operator reading iptables -S.
func spec(kind, iface string, mss int) []string {
	return []string{
		"-o", iface,
		"-p", "tcp",
		"--tcp-flags", "SYN,RST", "SYN",
		"-m", "comment", "--comment", "backpack-" + kind + "-mss-" + iface,
		"-j", "TCPMSS",
		"--set-mss", strconv.Itoa(mss),
	}
}
