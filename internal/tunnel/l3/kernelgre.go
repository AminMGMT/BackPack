package l3

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Kernel GRE.
//
// Every other carrier in this package is a datagram carrier: the engine reads
// IP packets from a TUN device, frames and seals them itself, and hands the
// result to a socket. Kernel GRE is the opposite arrangement. The kernel does
// the encapsulation — `ip link add … type gre` — and puts the packet on the
// wire as bare IP protocol 47, so there is no TUN, no session, no handshake and
// nothing for this process to carry. What is left for the engine is the link's
// lifetime: create it, address it, size it, watch it, and take it down again.
//
// # It is unencrypted, and that is the point of it
//
// The package doc explains why the engine was built around its own framing
// rather than the kernel's: protocol 47 is trivially blocked and carries
// everything in the clear. Both are still true. GRE is here because on a route
// that does pass it, it is the cheapest tunnel there is — no copy through
// userspace, no crypto, the kernel's own offloads — and an operator who runs
// WireGuard or TLS inside it loses nothing by its having no encryption of its
// own. Every screen that offers it says so in those words.
//
// # Why this is a path in the engine rather than a carrier
//
// A DatagramCarrier is "send this sealed datagram to that address". A kernel
// GRE link has nothing to send: the kernel sends. Wrapping it in the carrier
// interface would mean a carrier whose WriteTo is never called and whose
// Overhead lies about a session that does not exist. So the [l3] engine takes a
// second, much shorter path when the carrier is one of these two (runKernelGRE),
// and everything around it stays the same: the forwarder serves the same ports
// across the link and asks the same Up(), the metrics read the same Stats(),
// and the management screens find the same peer in the same snapshot.
//
// # Two names rather than a carrier and an underlay key
//
// "gre" and "ip6gre" are exactly the two kernel link types created, so the file
// says what the kernel runs. The underlay family is something both ends must
// agree on, and the carrier is already the one value every screen, the setup
// link and the Connection Test carry across and compare; a second key would be
// one more thing for the two ends to disagree about. The menus show one entry,
// GRE, and pick between the two from the other server's address.

// Kernel GRE carriers. They are accepted wherever a carrier is, but they are
// not datagram carriers — see the top of this file.
const (
	// CarrierGRE is a kernel GRE link over IPv4 endpoints.
	CarrierGRE = "gre"
	// CarrierIP6GRE is a kernel GRE link over IPv6 endpoints.
	CarrierIP6GRE = "ip6gre"
)

// KernelCarrier reports whether a carrier name is one of the kernel GRE links,
// which the engine runs without a TUN device, a session or a carrier socket.
func KernelCarrier(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case CarrierGRE, CarrierIP6GRE:
		return true
	}
	return false
}

// GRECarrierFor is the kernel GRE carrier for an endpoint of this family: gre
// for IPv4, ip6gre for IPv6.
func GRECarrierFor(ip net.IP) string {
	if ip != nil && ip.To4() == nil {
		return CarrierIP6GRE
	}
	return CarrierGRE
}

// GREOverhead is what the kernel adds to every packet on a GRE link: the outer
// IP header and the GRE header, which grows by four bytes when a key is set.
//
//	gre     20 + 4 = 24, 28 with a key
//	ip6gre  40 + 4 = 44, 48 with a key
//
// ip6gre's figure assumes the encapsulation limit option is off, which is how
// greLinkAddArgs creates the link; with the kernel's default it would be eight
// bytes more, spent on a hop limit for nested tunnels this one never has.
func GREOverhead(carrier string, key uint32) int {
	outer := 20
	if strings.EqualFold(strings.TrimSpace(carrier), CarrierIP6GRE) {
		outer = 40
	}
	gre := 4
	if key != 0 {
		gre = 8
	}
	return outer + gre
}

// greModule is the kernel module that provides a carrier's link type.
func greModule(carrier string) string {
	if carrier == CarrierIP6GRE {
		return "ip6_gre"
	}
	return "ip_gre"
}

// greLinkSpec is everything a GRE link is created with.
type greLinkSpec struct {
	Name    string
	Carrier string
	Local   net.IP
	Remote  net.IP
	Key     uint32
}

// greLinkAddArgs is the ip(8) command that creates the link, without the "ip".
//
// Kept pure, as the interface steps are, so what is asked of the kernel can be
// read and tested without a kernel to ask.
//
// ttl/hoplimit 255 rather than inherit: an inherited TTL leaks the inner
// packet's into the outer header, and a packet that has already crossed a few
// hops arrives at the tunnel with too little left to cross the path. The
// encapsulation limit is off on ip6gre for the reason GREOverhead gives.
// "key" sets the key both ways, which is what both ends of one tunnel use.
func greLinkAddArgs(s greLinkSpec) []string {
	args := []string{"link", "add", s.Name, "type", s.Carrier,
		"local", s.Local.String(), "remote", s.Remote.String()}
	if s.Carrier == CarrierIP6GRE {
		args = append(args, "hoplimit", "255", "encaplimit", "none")
	} else {
		args = append(args, "ttl", "255")
	}
	if s.Key != 0 {
		args = append(args, "key", strconv.FormatUint(uint64(s.Key), 10))
	}
	return args
}

// greLinkDelArgs removes the link, without the "ip".
func greLinkDelArgs(name string) []string { return []string{"link", "del", "dev", name} }

// greAcceptRule is the firewall rule body that lets this tunnel's GRE in:
// protocol 47 from the other server, and nothing else. The comment names the
// interface, so two tunnels to the same server hold a rule each and one
// stopping does not take the other's away.
func greAcceptRule(iface string, remote net.IP) []string {
	return []string{"INPUT", "-p", "47", "-s", remote.String(),
		"-m", "comment", "--comment", "backpack-gre " + iface, "-j", "ACCEPT"}
}

// Link types as /sys/class/net/<name>/type reports them (ARPHRD_*). A link of
// one of these under this tunnel's own name is one an earlier run of it left
// behind; anything else under the name belongs to somebody else.
const (
	arphrdIPGRE  = 778
	arphrdIP6GRE = 823
)

// explainGREAddFailure turns ip(8)'s answer to a failed link add into what the
// operator should do about it.
func explainGREAddFailure(s greLinkSpec, out string, err error) error {
	lower := strings.ToLower(out)
	cmd := "ip " + strings.Join(greLinkAddArgs(s), " ")
	switch {
	case strings.Contains(lower, "unknown device type"),
		strings.Contains(lower, "operation not supported"),
		strings.Contains(lower, "not supported"):
		module := greModule(s.Carrier)
		return fmt.Errorf("l3: the kernel has no %s support loaded (%s: %s). Load it with "+
			"`modprobe %s`, and to have it at every boot: echo %s > /etc/modules-load.d/backpack-gre.conf",
			s.Carrier, cmd, strings.TrimSpace(out), module, module)
	case strings.Contains(lower, "file exists"):
		key := "no key"
		if s.Key != 0 {
			key = fmt.Sprintf("key %d", s.Key)
		}
		return fmt.Errorf("l3: another GRE link already joins %s and %s with %s, and the kernel "+
			"allows one per pair and key. Each GRE tunnel between the same two servers needs a key "+
			"of its own — Backpack picks one for every tunnel it sets up; see `ip -d link show type %s`",
			s.Local, s.Remote, key, s.Carrier)
	case strings.Contains(lower, "operation not permitted"):
		return fmt.Errorf("l3: %s: %s (a GRE tunnel needs root or CAP_NET_ADMIN)", cmd, strings.TrimSpace(out))
	}
	return fmt.Errorf("l3: %s: %w: %s", cmd, err, strings.TrimSpace(out))
}

// validateGREEndpoints checks addr and gre_local for a kernel GRE tunnel and
// normalises addr to the bare host. A GRE link names both ends, so addr is the
// other server's address on either side, and it has no port.
func (c *Config) validateGREEndpoints() error {
	host := strings.TrimSpace(c.Addr)
	if h, p, err := net.SplitHostPort(host); err == nil {
		return fmt.Errorf("l3: carrier %q has no port: addr is the other server's address alone "+
			"(%q, not %q)", c.Carrier, h, net.JoinHostPort(h, p))
	}
	host = strings.Trim(host, "[]")
	if host == "" || strings.ContainsAny(host, " /") {
		return fmt.Errorf("l3: addr %q is not an IP address or a domain", c.Addr)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() {
			return fmt.Errorf("l3: addr %q cannot be a GRE endpoint: a GRE link names both servers, "+
				"so addr is the other server's real address on this side too", c.Addr)
		}
		if err := greFamilyFits(c.Carrier, "addr", ip); err != nil {
			return err
		}
	}
	c.Addr = host
	if local := strings.Trim(strings.TrimSpace(c.GRELocal), "[]"); local != "" {
		ip := net.ParseIP(local)
		if ip == nil {
			return fmt.Errorf("l3: gre_local %q is not an IP address of this server", c.GRELocal)
		}
		if err := greFamilyFits(c.Carrier, "gre_local", ip); err != nil {
			return err
		}
		c.GRELocal = ip.String()
	}
	if c.FEC.Data > 0 || c.FEC.Parity > 0 {
		return fmt.Errorf("l3: carrier %q cannot carry error correction: the kernel sends GRE itself, "+
			"and fec_data/fec_parity work only on Backpack's own carriers. Remove both keys", c.Carrier)
	}
	return nil
}

// greFamilyFits refuses an endpoint of the wrong family for the carrier.
func greFamilyFits(carrier, field string, ip net.IP) error {
	want := GRECarrierFor(ip)
	if want == carrier {
		return nil
	}
	if carrier == CarrierGRE {
		return fmt.Errorf("l3: %s %s is IPv6, and carrier %q is GRE over IPv4 — use carrier = %q",
			field, ip, CarrierGRE, CarrierIP6GRE)
	}
	return fmt.Errorf("l3: %s %s is IPv4, and carrier %q is GRE over IPv6 — use carrier = %q",
		field, ip, CarrierIP6GRE, CarrierGRE)
}

// greMTUFor is the interface MTU a GRE link should start with when the config
// leaves it to the engine: what the interface the route leaves by carries,
// minus what the kernel adds.
func greMTUFor(egressMTU int, carrier string, key uint32) int {
	if egressMTU <= 0 {
		egressMTU = 1500
	}
	mtu := egressMTU - GREOverhead(carrier, key)
	if mtu > maxMTU {
		mtu = maxMTU
	}
	if mtu < minMTU {
		mtu = minMTU
	}
	return mtu
}

// Liveness.
//
// A GRE link has no handshake, so nothing in the kernel says whether the other
// end is there: the link is "up" the moment it is created, with or without a
// peer. The engine finds out the way an operator would, by pinging the other
// end's tunnel address across the link, and the answers are what Up() and the
// management screens report.

const (
	// greKeepalive is how often the other end is pinged across the link.
	greKeepalive = 3 * time.Second

	// greSilentAfter is how long without an answer before the tunnel is
	// reported down. The same fifteen seconds the session carriers use, for
	// the same reason: long enough not to flap on a lost reply or two.
	greSilentAfter = 15 * time.Second

	// grePingTimeout bounds one ping.
	grePingTimeout = 2 * time.Second
)

// greState is what a kernel GRE tunnel keeps between generations: when the
// other end last answered, and the traffic counters of links that are gone.
// The link's own counters start from zero each time it is created, and the
// metrics want totals that survive a restart, as the session carriers' do.
type greState struct {
	lastAnswer atomic.Int64 // unix nanoseconds; zero before any answer

	// base is the sum of every closed link's counters.
	base struct {
		bytesIn, bytesOut     atomic.Uint64
		packetsIn, packetsOut atomic.Uint64
		dropped               atomic.Uint64
	}
	// iface is the link currently open, or empty.
	iface atomic.Pointer[string]
}

// up reports whether the other end answered recently.
func (g *greState) up(now time.Time) bool {
	last := g.lastAnswer.Load()
	return last != 0 && now.Sub(time.Unix(0, last)) < greSilentAfter
}

// greCounters is one reading of a link's counters.
type greCounters struct {
	bytesIn, bytesOut     uint64
	packetsIn, packetsOut uint64
	dropped               uint64
}

// stats adds the open link's counters, read by read, to what closed links
// carried.
func (g *greState) stats(read func(iface string) (greCounters, bool)) Stats {
	s := Stats{
		BytesIn:    g.base.bytesIn.Load(),
		BytesOut:   g.base.bytesOut.Load(),
		PacketsIn:  g.base.packetsIn.Load(),
		PacketsOut: g.base.packetsOut.Load(),
		Dropped:    g.base.dropped.Load(),
	}
	if name := g.iface.Load(); name != nil && read != nil {
		if c, ok := read(*name); ok {
			s.BytesIn += c.bytesIn
			s.BytesOut += c.bytesOut
			s.PacketsIn += c.packetsIn
			s.PacketsOut += c.packetsOut
			s.Dropped += c.dropped
		}
	}
	return s
}

// retire folds a closing link's last reading into the totals.
func (g *greState) retire(last greCounters) {
	g.iface.Store(nil)
	g.base.bytesIn.Add(last.bytesIn)
	g.base.bytesOut.Add(last.bytesOut)
	g.base.packetsIn.Add(last.packetsIn)
	g.base.packetsOut.Add(last.packetsOut)
	g.base.dropped.Add(last.dropped)
}

// errGREGone is a generation's answer when its link was removed from under it.
var errGREGone = errors.New("l3: the GRE link was removed while the tunnel was running — creating it again")

// greSearchMTU binary-searches for the largest packet that crosses, given a
// way to test one size, between floor and ceiling. ceiling is tried first, so
// a clean path costs one probe. It reports false when not even floor crosses,
// which is a tunnel with nobody answering rather than a measurement.
func greSearchMTU(floor, ceiling int, fits func(int) bool) (int, bool) {
	if ceiling < floor {
		return 0, false
	}
	if fits(ceiling) {
		return ceiling, true
	}
	if !fits(floor) {
		return 0, false
	}
	best := floor
	lo, hi := floor+1, ceiling-1
	for lo <= hi {
		mid := (lo + hi) / 2
		if fits(mid) {
			best, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	// Every search that finds anything ends below ceiling — the interface's own
	// MTU — and the probe loop lowers the link to it, which nothing ever raises
	// again. So the result is asked to prove itself first: a path that blacked
	// out partway through failed every probe sent during the outage, and the
	// search read each failure as "too big". If floor no longer crosses, the
	// path is down and there is no measurement; if ceiling crosses now, the
	// path never shrank. Only a figure that survives both is a narrower path.
	if !fits(floor) {
		return 0, false
	}
	if fits(ceiling) {
		return ceiling, true
	}
	return best, true
}

// GRELocalToward is the address this server sends from to reach remote: the
// source the kernel picks for that route. Connecting a UDP socket sends
// nothing; it only asks the routing table. Exported so the setup screens put
// the same address in the setup link that the link will be created with.
func GRELocalToward(remote net.IP) (net.IP, error) {
	network := "udp4"
	if remote.To4() == nil {
		network = "udp6"
	}
	c, err := net.DialUDP(network, nil, &net.UDPAddr{IP: remote, Port: 9})
	if err != nil {
		return nil, fmt.Errorf("l3: this server has no route to %s, so the GRE link has no local "+
			"address to use (%v) — check the address, or set gre_local", remote, err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP, nil
}
