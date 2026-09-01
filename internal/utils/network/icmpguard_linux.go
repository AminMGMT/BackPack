//go:build linux

package network

import (
	"fmt"
	"os/exec"
	"strconv"
)

// The icmp spoof profile carries its data inside ICMP Echo Requests. A raw
// socket lifts those out, but the host kernel ALSO sees each one — it is a real
// echo request addressed to this host's real IP — and answers it with an Echo
// Reply to the (forged) source. That reply is useless to the tunnel, and it is
// expensive: it echoes the request's payload, so on the download path it is one
// full-sized packet out for every full-sized data packet in — the uplink cost
// doubles — and a host answering pings it never sent is a plain fingerprint.
//
// The obvious silence is net.ipv4.icmp_echo_ignore_all=1, which the reference
// spoof-tunnel uses and this carrier used to. It works, and it is too broad: it
// is a per-namespace switch that stops the kernel answering EVERY echo request,
// including the ones that arrive on the tunnel itself. A layer-3 tunnel is a
// private network, and ping across it — health checks, path-MTU discovery,
// mtr — is a first-class thing to do; the global switch silences all of it.
// Measured on a loopback pair: with the global switch, ping across the tunnel
// is 100% loss while TCP is fine.
//
// icmpEchoGuard is the narrow version, modelled on rstGuard. It installs one
// iptables rule that drops exactly the kernel's auto-replies to THIS carrier's
// requests — outbound Echo Replies whose ICMP identifier is the tunnel's, which
// the carrier stamps as its port. A ping across the tunnel carries a different
// identifier and is untouched. The kernel still stops wasting the uplink; the
// tunnel's own ICMP still works.
//
// It matches the identifier with the u32 module, because iptables has no native
// icmp-id match. If u32 is unavailable the rule does not install, and — unlike
// the old behaviour — nothing falls back to the global switch: a tunnel that
// works and wastes some uplink is a better failure than one whose private
// network cannot carry a ping. The carrier logs which happened.
type icmpEchoGuard struct {
	port   uint16
	rule   []string
	active bool
}

// installICMPEchoGuard adds the reply-drop rule for a tunnel port and returns a
// handle that remembers whether it took, so remove() only undoes what it added.
func installICMPEchoGuard(port uint16) *icmpEchoGuard {
	g := &icmpEchoGuard{port: port, rule: icmpEchoRule(port)}
	if _, err := exec.LookPath("iptables"); err != nil {
		return g
	}
	args := append([]string{"-I"}, g.rule...)
	if err := exec.Command("iptables", args...).Run(); err == nil {
		g.active = true
	}
	return g
}

// remove deletes the rule if it was installed. Safe on a guard that installed
// nothing.
func (g *icmpEchoGuard) remove() {
	if g == nil || !g.active {
		return
	}
	args := append([]string{"-D"}, g.rule...)
	_ = exec.Command("iptables", args...).Run()
	g.active = false
}

// Installed reports whether the rule is in place, so the carrier can log which
// of the two outcomes it got.
func (g *icmpEchoGuard) Installed() bool { return g != nil && g.active }

// icmpEchoRule is the iptables rule body: drop outbound Echo Replies whose ICMP
// identifier equals the tunnel port.
//
// The u32 expression reads the identifier out of a variable-length IP header:
//
//	0>>22&0x3C   the low nibble of the first IP byte (IHL) times four — the IP
//	            header length in bytes, i.e. where the ICMP header begins
//	@4          load the 32-bit word four bytes into the ICMP header: id and seq
//	>>16        keep the high half — the 16-bit identifier
//	=port       match the tunnel's identifier
//
// Tagged with the same comment shape rstRule uses, so a rule left behind by a
// crash is identifiable and removable by hand.
func icmpEchoRule(port uint16) []string {
	p := strconv.Itoa(int(port))
	return []string{
		"OUTPUT",
		"-p", "icmp",
		"--icmp-type", "echo-reply",
		"-m", "u32", "--u32", fmt.Sprintf("0>>22&0x3C@4>>16=%d", port),
		"-m", "comment", "--comment", fmt.Sprintf("backpack-spoof-icmp-%s", p),
		"-j", "DROP",
	}
}
