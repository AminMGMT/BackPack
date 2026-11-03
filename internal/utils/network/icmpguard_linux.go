//go:build linux

package network

import (
	"fmt"
	"net"
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
// In reply mode (spoof_icmp_reply) the server's own uplink is Echo Replies
// carrying that same identifier, so type and identifier alone cannot tell its
// data from the kernel's answers. The carrier therefore marks its send socket,
// and the rule leaves marked packets alone. The mark is source-agnostic: it
// works whatever spoof_src_ip / spoof_src_pool rotate through, and with no
// forged source at all.
//
// A mark is also something `ip rule fwmark` matches, by value/mask and either
// way round, so it can change routing. The carrier does not trust a constant to
// be safe: pickICMPReplyMark looks up the route under each candidate and keeps
// only one that yields the route unmarked traffic gets.
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
	note   string // why the rule did not take, when it did not
}

// spoofReplyMark is the mark the carrier prefers to stamp on its own outgoing
// Echo Replies so the guard can tell them from the kernel's automatic answers.
// It is set only on the carrier's send socket, and only in the one
// configuration where the two are otherwise identical: the server's uplink is
// Echo Replies (reply mode) and the guard is needed.
//
// It is deliberately distinctive — outside the small integers an operator's
// so_mark picks — but distinctive is not the same as safe: `fwmark 0x1/0x1`
// matches every odd mark. So this is only the first candidate, kept whenever no
// fwmark rule can tell marks apart at all. The mark is local: never on the wire.
const spoofReplyMark = 0x4241434b

// installICMPEchoGuard adds the reply-drop rule for a tunnel port and returns a
// handle that remembers whether it took, so remove() only undoes what it added.
// A zero mark leaves the rule with no exemption, which is what a side whose
// uplink is not Echo Replies needs.
func installICMPEchoGuard(port uint16, mark uint32) *icmpEchoGuard {
	// Bodies a crashed run of an earlier version left behind: one from before
	// the mark existed, and one whose exemption was inverted around the
	// preferred constant. This process installs neither, so a match is stale —
	// unless another live carrier here still holds it.
	for _, stale := range [][]string{legacyICMPEchoRule(port), previousMarkedICMPEchoRule(port)} {
		if !ruleInUse(stale) {
			removeStaleEchoRule(stale)
		}
	}
	return installEchoRule(&icmpEchoGuard{port: port, rule: icmpEchoRule(port, mark)})
}

// ruleInUse reports whether some carrier in this process holds rule.
func ruleInUse(rule []string) bool {
	echoRules.Lock()
	defer echoRules.Unlock()
	return echoRules.users[strings.Join(rule, " ")] > 0
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

// installEchoRule inserts a guard's rule and records whether it took — and why
// it did not, in g.note, so the carrier can say so instead of leaving an
// operator to guess whether the kernel's replies are being answered.
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
		g.note = "iptables was not found"
		return g
	}
	// A rule already there is one a crashed run left behind — the process
	// died before it could remove it. It is adopted rather than doubled, so
	// crash after crash does not stack copies, and this run's exit removes it.
	if exec.Command("iptables", append([]string{"-C"}, g.rule...)...).Run() == nil {
		g.active = true
	} else if out, err := exec.Command("iptables", append([]string{"-I"}, g.rule...)...).CombinedOutput(); err == nil {
		g.active = true
	} else {
		g.note = fmt.Sprintf("iptables refused the rule: %s", strings.TrimSpace(string(out)))
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

// icmpEchoGuardPlan is what a carrier with these directions needs from the
// guard.
type icmpEchoGuardPlan struct {
	// suppress installs the rule: the kernel answers an inbound Echo Request
	// with one of its own, and nothing should carry that reply.
	suppress bool
	// marked exempts the carrier's own replies: its uplink is Echo Replies
	// carrying the same identifier as the kernel's answers.
	marked bool
}

// planICMPEchoGuard decides the guard from the send and receive profiles, which
// need not match. The kernel only answers IPv4 ICMP Echo Requests — a receive
// profile that accepts Replies, or rides protocol 58 (icmpv6), generates
// nothing to suppress — and the carrier only collides with the rule when its
// own uplink is an IPv4 Echo Reply, which is the server in reply mode.
func planICMPEchoGuard(send, recv SpoofProfile, sendType, recvType byte) icmpEchoGuardPlan {
	return icmpEchoGuardPlan{
		suppress: recv == SpoofProfileICMP && recvType == icmpTypeEchoRequest,
		marked:   send == SpoofProfileICMP && sendType == icmpTypeEchoReply,
	}
}

// icmpEchoRule is the iptables rule body: drop outbound Echo Replies whose ICMP
// identifier equals the tunnel port. With a mark, only the UNMARKED ones are
// dropped — the kernel's automatic answers, which carry no mark — so the
// carrier's own marked replies pass. Matching "mark 0" rather than "not our
// mark" keeps two carriers' rules compatible: whatever mark each stamps, the
// other's rule leaves it alone.
//
// A zero mark leaves the exemption out: the caller has no replies of its own to
// protect.
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
func icmpEchoRule(port uint16, mark uint32) []string {
	p := strconv.Itoa(int(port))
	rule := []string{
		"OUTPUT",
		"-p", "icmp",
		"--icmp-type", "echo-reply",
		"-m", "u32", "--u32", fmt.Sprintf("0>>22&0x3C@4>>16=%d", port),
	}
	if mark != 0 {
		rule = append(rule, "-m", "mark", "--mark", "0x0")
	}
	return append(rule,
		"-m", "comment", "--comment", fmt.Sprintf("backpack-spoof-icmp-%s", p),
		"-j", "DROP",
	)
}

// pickICMPReplyMark returns the mark the carrier should stamp on its own Echo
// Replies, or the reason there is no safe one. iface is the device the send
// socket is pinned to, if any, so the lookup is made under the same constraint.
//
// `ip rule fwmark` matches value/mask, and either way round, so a constant —
// however distinctive — can collide with a policy written for other traffic:
// `fwmark 0x1/0x1` catches every odd mark, `not fwmark 0x0/0x1` likewise.
// Parsing those rules is beside the point; their outcome is what matters. Each
// candidate is looked up exactly as the kernel would look it up, and kept only
// if the route to the peer is the one unmarked traffic gets. On a host with no
// fwmark rule at all — the usual case — nothing can tell marks apart, and the
// preferred constant is kept without asking the kernel anything.
func pickICMPReplyMark(peer net.IP, iface string) (uint32, string) {
	rules, err := exec.Command("ip", "-4", "rule", "show").Output()
	if err != nil {
		return 0, fmt.Sprintf("the host's routing rules could not be read (%v), so a firewall mark cannot be shown to leave routing unchanged", err)
	}
	if !strings.Contains(string(rules), "fwmark") {
		return spoofReplyMark, ""
	}
	base, err := routeByMark(peer, iface, 0)
	if err != nil {
		return 0, fmt.Sprintf("the route to the peer could not be read (%v)", err)
	}
	mark, ok := chooseICMPReplyMark(func(m uint32) bool {
		got, err := routeByMark(peer, iface, m)
		return err == nil && got == base
	})
	if !ok {
		return 0, "an ip rule fwmark (masked or inverted) sends every candidate mark somewhere other than where unmarked traffic goes"
	}
	return mark, ""
}

// icmpReplyMarkCandidates is what pickICMPReplyMark tries, in order: the
// preferred constant, then single-bit marks.
func icmpReplyMarkCandidates() []uint32 {
	out := []uint32{spoofReplyMark}
	for bit := 1; bit < 31; bit++ {
		out = append(out, 1<<bit)
	}
	return out
}

// chooseICMPReplyMark is the first candidate safe accepts.
func chooseICMPReplyMark(safe func(uint32) bool) (uint32, bool) {
	for _, mark := range icmpReplyMarkCandidates() {
		if safe(mark) {
			return mark, true
		}
	}
	return 0, false
}

// routeByMark is the kernel's route lookup for peer under mark — with mark 0
// meaning no mark at all — normalized so the two can be compared: the mark
// itself is dropped from the answer, since that is the one expected difference.
func routeByMark(peer net.IP, iface string, mark uint32) (string, error) {
	args := []string{"-4", "route", "get", peer.String()}
	if iface != "" {
		args = append(args, "oif", iface)
	}
	if mark != 0 {
		args = append(args, "mark", fmt.Sprintf("0x%x", mark))
	}
	out, err := exec.Command("ip", args...).Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	kept := fields[:0]
	for i := 0; i < len(fields); i++ {
		if fields[i] == "mark" && i+1 < len(fields) {
			i++
			continue
		}
		kept = append(kept, fields[i])
	}
	return strings.Join(kept, " "), nil
}

// legacyICMPEchoRule is the guard's body before the mark was added, kept
// only so an upgrade can clear one that a crashed older run left in the chain.
func legacyICMPEchoRule(port uint16) []string {
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

// previousMarkedICMPEchoRule is the body this guard shipped with when the mark
// was a fixed constant and the exemption read "anything but that mark". Kept
// only so an upgrade can clear one a crashed run left behind: two such rules
// with different marks would drop each other's packets.
func previousMarkedICMPEchoRule(port uint16) []string {
	p := strconv.Itoa(int(port))
	return []string{
		"OUTPUT",
		"-p", "icmp",
		"--icmp-type", "echo-reply",
		"-m", "u32", "--u32", fmt.Sprintf("0>>22&0x3C@4>>16=%d", port),
		"-m", "mark", "!", "--mark", fmt.Sprintf("0x%08x", uint32(spoofReplyMark)),
		"-m", "comment", "--comment", fmt.Sprintf("backpack-spoof-icmp-%s", p),
		"-j", "DROP",
	}
}

// removeStaleEchoRule deletes a rule if it is there, best effort.
func removeStaleEchoRule(rule []string) {
	if _, err := exec.LookPath("iptables"); err != nil {
		return
	}
	if exec.Command("iptables", append([]string{"-C"}, rule...)...).Run() == nil {
		_ = exec.Command("iptables", append([]string{"-D"}, rule...)...).Run()
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
