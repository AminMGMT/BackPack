//go:build linux

package network

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
)

// What the paqet-based PCK engine (internal/tunnel/paqet) needs from the host,
// and nothing else: where a frame leaves from, and the firewall rules that keep
// the kernel out of the flow.
//
// paqet makes its operator find these by hand — the interface, the local
// address, the gateway's MAC, and three iptables lines — and a wrong one gives
// a tunnel that connects and carries nothing. The lookup and the rule set are
// the code the previous PCK carrier already had, written once for exactly this
// reason; this file only exposes them.

// PckPath is how a frame leaves this machine toward the peer.
type PckPath struct {
	// Iface is the egress device and LocalIP the address the route to the peer
	// sends from.
	Iface   string
	LocalIP net.IP
	// SrcMAC and NextHop address the Ethernet header. Both are nil on a link
	// with no L2 header (a tunnel, PPP), where the frame is the IP packet.
	SrcMAC, NextHop net.HardwareAddr
	Ethernet        bool
}

// DiscoverPckPath resolves the route toward the peer into a PckPath.
//
// peer is nil on the listening side, which has no single peer: any public
// address finds the default route, which is where its replies will go. iface
// and gatewayMAC override the lookup, for a host where it is wrong.
func DiscoverPckPath(peer net.IP, iface, gatewayMAC string) (*PckPath, error) {
	toward := peer.To4()
	server := toward == nil
	if server {
		toward = net.IPv4(1, 1, 1, 1).To4()
	}
	egress, err := discoverEgress(toward, iface, gatewayMAC)
	if err != nil && server && iface == "" {
		// The listener has no peer, so a route to a stand-in address that does
		// not exist is not a reason to refuse. See newPckConn for the long
		// version: any usable interface will carry replies, and a wrong guess
		// is recoverable with pck_interface.
		if name := firstUsableIface(); name != "" {
			egress, err = discoverEgress(toward, name, gatewayMAC)
		}
	}
	if err != nil {
		return nil, err
	}
	p := &PckPath{
		Iface:    egress.Iface.Name,
		LocalIP:  egress.LocalIP,
		SrcMAC:   egress.SrcMAC,
		NextHop:  egress.NextHop,
		Ethernet: egress.Ethernet,
	}
	// Only a dialler needs it now: it has a peer to find a gateway for. A
	// listener with none learns each client's next hop from what arrives.
	if p.Ethernet && p.NextHop == nil && peer != nil {
		return nil, fmt.Errorf("pck: the next hop's MAC address on %s is not in the neighbour table "+
			"(the gateway did not answer ARP); set pck_gateway_mac to the gateway's address", p.Iface)
	}
	return p, nil
}

// PckLease is one engine's claim on the host for the life of a generation: its
// source ports, held so the kernel hands them to nothing else, and the firewall
// rules that drop the RSTs the kernel would answer the flow with and keep the
// segments out of conntrack.
type PckLease struct {
	id     string
	server bool
	base   uint16
	lo, hi uint16
	local  net.IP
	guard  *pckGuard

	mu       sync.Mutex
	reserved map[uint16]*os.File
}

// LeasePckPorts claims the ports and installs the rules.
//
// On the listening side port is the tunnel port, reserved now. On the dialling
// side it is the peer's port, and each source port is claimed by ClaimPort out
// of a range the rules already cover. peer scopes the rules to the other server
// where it is known.
func LeasePckPorts(token string, server bool, port uint16, path *PckPath, peer net.IP) (*PckLease, error) {
	l := &PckLease{
		id: pckTunnelID(token), server: server, local: path.LocalIP,
		reserved: map[uint16]*os.File{},
	}
	legacyBase := legacyPckClientPortBase(token)
	legacy := [][][]string{pckRules("", port, port)}
	if server {
		l.lo, l.hi = port, port
		f, err := reservePckPort(path.LocalIP, port)
		if err != nil {
			return nil, err
		}
		l.reserved[port] = f
	} else {
		l.base = pckClientPortBase(token)
		l.lo, l.hi = l.base, l.base+pckPortSpan-1
		legacy = [][][]string{pckRules("", legacyBase, legacyBase+pckPortSpan-1)}
	}
	peerScope := ""
	if peer != nil {
		peerScope = peer.String()
	}
	rules := scopedPckRules(l.id, l.lo, l.hi, path.Iface, path.LocalIP.String(), peerScope, port)
	g, err := installPckGuard(l.id, legacy, rules)
	if err != nil {
		l.Close()
		return nil, err
	}
	l.guard = g
	return l, nil
}

// ClaimPort takes a free source port of the client's range. It is an error on
// the listening side, whose one port is fixed.
func (l *PckLease) ClaimPort() (uint16, error) {
	if l.server {
		return 0, fmt.Errorf("pck: a listener has one port, it is not claimed")
	}
	port, f, err := reservePckClientPort(l.id, l.base, l.local)
	if err != nil {
		return 0, err
	}
	l.mu.Lock()
	l.reserved[port] = f
	l.mu.Unlock()
	return port, nil
}

// ReleasePort gives a claimed source port back.
func (l *PckLease) ReleasePort(port uint16) {
	l.mu.Lock()
	f := l.reserved[port]
	delete(l.reserved, port)
	l.mu.Unlock()
	if f != nil {
		f.Close()
	}
	if !l.server && f != nil {
		releasePckClientPort(l.id, port)
	}
}

// PortRange is the span of ports the rules cover and the capture filters on.
func (l *PckLease) PortRange() (lo, hi uint16) { return l.lo, l.hi }

// GuardInstalled reports whether every firewall rule is in place, so the engine
// can say when it is running without them.
func (l *PckLease) GuardInstalled() bool { return l.guard.Installed() }

// Close releases every port and removes the rules (the last lease out of a
// shared rule set removes it).
func (l *PckLease) Close() {
	l.mu.Lock()
	ports := make([]uint16, 0, len(l.reserved))
	for p := range l.reserved {
		ports = append(ports, p)
	}
	l.mu.Unlock()
	for _, p := range ports {
		l.ReleasePort(p)
	}
	l.guard.remove()
}

// PckGuardStatus is what this host has for one PCK tunnel's firewall guard: the
// rules that drop the kernel's RSTs to the tunnel's segments and keep them out
// of connection tracking. It is read from the tables, not from the process, so
// the diagnosis sees what the kernel does rather than what the engine believes.
type PckGuardStatus struct {
	// Iptables is whether the iptables binary exists. Without it the engine
	// runs without the guard, and the flow drops at the first stateful hop.
	Iptables bool
	// Rules is how many of the tunnel's three rules are installed: the RST
	// drop in filter OUTPUT and the two NOTRACK rules in raw. Three is whole.
	Rules int
	// Err is why the tables could not be read (not root, say).
	Err error
}

// PckGuardWant is the number of rules a whole guard has.
const PckGuardWant = 3

// PckGuardState reads the rules that belong to the tunnel holding token.
func PckGuardState(token string) PckGuardStatus {
	var st PckGuardStatus
	if _, err := exec.LookPath("iptables"); err != nil {
		return st
	}
	st.Iptables = true
	prefix := pckRulePrefix(pckTunnelID(token))
	for _, table := range []string{"filter", "raw"} {
		out, err := pckIptables("-t", table, "-S")
		if err != nil {
			st.Err = err
			return st
		}
		st.Rules += countPckRules(string(out), prefix)
	}
	return st
}

// countPckRules counts the rules of an `iptables -S` listing whose comment
// starts with prefix.
func countPckRules(listing, prefix string) int {
	return len(tunnelRuleDeletions(listing, prefix))
}
