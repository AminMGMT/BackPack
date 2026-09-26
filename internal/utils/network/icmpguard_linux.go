//go:build linux

package network

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
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
	return installEchoRule(&icmpEchoGuard{port: port, rule: icmpEchoRule(port)})
}

// installXdiEchoGuard is the same guard for the xdi carrier's server side.
//
// xdi carries its client-to-server traffic in Echo Requests too, and the
// server's kernel answered every one of them with an Echo Reply echoing the
// whole payload. The client throws those away — they carry its own direction
// byte, not the server's (see icmpframe.go) — but by then they have been sent:
// every byte a user uploads through an xdi tunnel went back out of the server
// again, doubling its uplink and the kernel work on both ends. The rule drops
// them before they leave.
//
// It cannot match on the identifier the way the spoof guard does, because xdi
// clients draw theirs at random. What every packet of the tunnel carries is its
// tag, and what only the kernel's copies carry on the way out is the client's
// direction byte — the server's own replies are stamped with its own. So the
// rule matches the two together.
func installXdiEchoGuard(tag [xdiTagLen]byte) *icmpEchoGuard {
	return installEchoRule(&icmpEchoGuard{rule: xdiEchoRule(tag)})
}

// installEchoRule inserts a guard's rule and records whether it took.
func installEchoRule(g *icmpEchoGuard) *icmpEchoGuard {
	key := strings.Join(g.rule, " ")
	echoRules.Lock()
	defer echoRules.Unlock()
	// Shared within the process: the sessions of one reverse xdi tunnel each
	// open a carrier with the same tag, and so the same rule. The first puts
	// it in, the last takes it out; one session closing must not pull it from
	// under the others.
	if echoRules.users[key] > 0 {
		echoRules.users[key]++
		g.active = true
		return g
	}
	if _, err := exec.LookPath("iptables"); err != nil {
		return g
	}
	// A rule already there is one a crashed run left behind — the process
	// died before it could remove it. It is adopted rather than doubled, so
	// crash after crash does not stack copies, and this run's exit removes it.
	if exec.Command("iptables", append([]string{"-C"}, g.rule...)...).Run() == nil {
		g.active = true
	} else if exec.Command("iptables", append([]string{"-I"}, g.rule...)...).Run() == nil {
		g.active = true
	}
	if g.active {
		echoRules.users[key] = 1
	}
	return g
}

// echoRules counts, per rule, the carriers in this process that rely on it.
var echoRules = struct {
	sync.Mutex
	users map[string]int
}{users: map[string]int{}}

// remove deletes the rule once the last carrier relying on it lets go. Safe on
// a guard that installed nothing, and on one removed twice.
func (g *icmpEchoGuard) remove() {
	if g == nil || !g.active {
		return
	}
	g.active = false
	key := strings.Join(g.rule, " ")
	echoRules.Lock()
	defer echoRules.Unlock()
	if echoRules.users[key]--; echoRules.users[key] > 0 {
		return
	}
	delete(echoRules.users, key)
	_ = exec.Command("iptables", append([]string{"-D"}, g.rule...)...).Run()
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

// xdiEchoRule drops outbound Echo Replies that carry this tunnel's tag and the
// client's direction byte — the kernel's automatic answers to the client's
// requests, and nothing the server itself sends.
//
//	0>>22&0x3C   the IP header length, where the ICMP header begins
//	@8           the first word of the echo data: the tunnel's four-byte tag
//	@12>>24      the byte after it: the direction marker
func xdiEchoRule(tag [xdiTagLen]byte) []string {
	word := uint32(tag[0])<<24 | uint32(tag[1])<<16 | uint32(tag[2])<<8 | uint32(tag[3])
	return []string{
		"OUTPUT",
		"-p", "icmp",
		"--icmp-type", "echo-reply",
		"-m", "u32", "--u32", fmt.Sprintf("0>>22&0x3C@8=0x%08x&&0>>22&0x3C@12>>24=0x%02x", word, xdiDirClient),
		"-m", "comment", "--comment", fmt.Sprintf("backpack-xdi-echo-%08x", word),
		"-j", "DROP",
	}
}

// installXdiAcceptRule lets this tunnel's own echoes through a firewall that
// drops ICMP.
//
// xdi reads from a raw ICMP socket, which is handed a packet only after the
// INPUT chain has passed it. Servers in Iran commonly drop ICMP so as not to
// answer ping, and on such a server an xdi tunnel started, logged nothing
// wrong, and never completed a handshake. The rule accepts only echoes that
// carry this tunnel's tag and the other end's direction byte — the server
// accepts the client's requests, the client the server's replies — so the
// host stays as silent to ping as it was. Inserted at the top of INPUT so a
// drop further down cannot pre-empt it, and removed when the carrier closes.
func installXdiAcceptRule(tag [xdiTagLen]byte, server bool) *icmpEchoGuard {
	return installEchoRule(&icmpEchoGuard{rule: xdiAcceptRule(tag, server)})
}

// xdiAcceptRule is the INPUT rule body for installXdiAcceptRule.
func xdiAcceptRule(tag [xdiTagLen]byte, server bool) []string {
	word := uint32(tag[0])<<24 | uint32(tag[1])<<16 | uint32(tag[2])<<8 | uint32(tag[3])
	typ, dir, side := "echo-reply", xdiDirServer, "client"
	if server {
		typ, dir, side = "echo-request", xdiDirClient, "server"
	}
	return []string{
		"INPUT",
		"-p", "icmp",
		"--icmp-type", typ,
		"-m", "u32", "--u32", fmt.Sprintf("0>>22&0x3C@8=0x%08x&&0>>22&0x3C@12>>24=0x%02x", word, dir),
		"-m", "comment", "--comment", fmt.Sprintf("backpack-xdi-in-%s-%08x", side, word),
		"-j", "ACCEPT",
	}
}
