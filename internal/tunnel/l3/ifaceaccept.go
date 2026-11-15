package l3

// Letting the tunnel's own traffic in.
//
// The rules for GRE (protocol 47 from the other server) and xdi (its ICMP)
// let the carrier through a host firewall whose INPUT policy is DROP, and the
// link then came up and was useless: what arrives inside it — the forwarded
// ports' connections the Iran side dials to the kharej's tunnel address, and
// the pings that tell the screens the other end is there — enters INPUT on the
// tunnel interface, which that same policy drops. Measured in two namespaces
// with `-P INPUT DROP` (loopback and established flows allowed, as a hardened
// VPS has): GRE and xdi both up, every packet across them dropped.
//
// So the tunnel lets in what arrives on its own interface, for as long as it
// runs: one rule, tagged with the interface's name so two tunnels hold one
// each. That is no wider than the tunnel already is — whatever reaches the
// interface came from the other server of this tunnel, through its key or
// token. Only IPv4: the inner addresses are IPv4.

// ifaceAcceptComment tags the rule of one interface.
func ifaceAcceptComment(iface string) string { return "backpack-l3 " + iface }

// ifaceAcceptRule is the rule body, without the -I/-D/-C.
func ifaceAcceptRule(iface string) []string {
	return []string{"INPUT", "-i", iface, "-m", "comment", "--comment", ifaceAcceptComment(iface), "-j", "ACCEPT"}
}
