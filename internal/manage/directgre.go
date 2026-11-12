package manage

import (
	"fmt"
	"math"
	"math/rand"
	"net"
	"strings"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/tunnel/l3"
)

// Setting up a kernel GRE tunnel.
//
// It is a direct tunnel like the others — the same Iran-first setup link, the
// same forwarded ports, presets, listing, health and Connection Test — and the
// questions are fewer, because a GRE link has less to agree on: no port, no
// token, no error correction. What it does have is one fact the other carriers
// never needed: each end must name the other's real address, since a GRE link
// names both of its ends. The Iran side has the kharej's; the setup link takes
// Iran's across.
//
// Everything else is chosen here rather than asked: the interface name, the
// /30, and the GRE key. The key is the part that makes several GRE tunnels
// between the same two servers possible at all — the kernel allows one link per
// pair of addresses and key — so every tunnel gets one of its own, and the link
// carries it so both ends match.

// greUnencryptedWarning is what the wizard says before anything else, in the
// owner's words: plainly, and on the screen where GRE is chosen.
const greUnencryptedWarning = "GRE Is Not Encrypted. It Travels As Bare IP Protocol 47, And Whatever " +
	"Crosses It Is In The Clear Unless It Is Encrypted Itself (WireGuard, TLS, SSH)."

// setupGRE is the wizard for one side of a GRE tunnel set up by hand: the
// whole of the Iran side, and the kharej side when there is no setup link.
func setupGRE(side directSide) {
	fmt.Println()
	tui.Warn(greUnencryptedWarning)
	fmt.Println()

	cfg := l3Spec{Side: side, Carrier: l3.CarrierGRE, Encap: "gre"}
	cfg.LocalIP, cfg.PeerIP = freeL3Subnet(side)

	other, there := "Kharej", "kharej"
	if side == sideKharej {
		other, there = "Iran", "Iran"
	}
	host := strings.Trim(strings.TrimSpace(tui.Prompt(other+" IP Or Domain: ")), "[]")
	if host == "" {
		tui.Error("An address is required.")
		tui.PressEnter()
		return
	}
	carrier, err := greCarrierForHost(host, "")
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	cfg.Carrier, cfg.Addr = carrier, host

	if side == sideIran {
		if raw := tui.Prompt("Forwarded Ports (Blank For TUN): "); strings.TrimSpace(raw) != "" {
			cfg.Ports = parsePorts(raw)
			if err := validatePortSpecs(cfg.Ports); err != nil {
				tui.Error(err.Error())
				tui.PressEnter()
				return
			}
		}
		cfg.GREKey = freeGREKey()
	} else {
		// Exactly as the Iran server printed them: this machine's comes first.
		cfg.LocalIP = tui.PromptDefault("This Server's Tunnel Address", cfg.LocalIP)
		cfg.PeerIP = tui.PromptDefault("The Iran Server's Tunnel Address", cfg.PeerIP)
		for l3.CheckTunnelEnds(cfg.LocalIP, cfg.PeerIP) != nil {
			tui.Error("The Iran server's address cannot be this machine's own (" +
				hostOnly(cfg.LocalIP) + "). It is the address the Iran server has on the tunnel.")
			cfg.PeerIP = tui.PromptDefault("The Iran Server's Tunnel Address", "")
		}
		key, ok := askGREKeyFrom(there)
		if !ok {
			return
		}
		cfg.GREKey = key
	}
	if other := greTunnelJoining(cfg.Addr, cfg.GREKey); other != "" {
		tui.Error("Tunnel " + other + " on this server already joins " + cfg.Addr +
			" with GRE key " + fmt.Sprint(cfg.GREKey) + " — the kernel allows one per key. Use another key.")
		tui.PressEnter()
		return
	}

	cfg.Name = uniqueName(tui.PromptDefault("Tunnel Name", freeName(cfg.DefaultName())))

	if side == sideIran && len(cfg.Ports) > 0 {
		cfg.AcceptUDP = tui.Confirm("Carry UDP As Well As TCP On Those Ports", false)
		cfg.Ports = offerL3Sharing(cfg)
		if busy := busyForwardPorts(cfg.Ports, cfg.PeerIP); len(busy) > 0 {
			tui.Error("Already in use on this server: " + strings.Join(busy, ", ") +
				" — the web panel's own port is the usual one. Pick other ports.")
			tui.PressEnter()
			return
		}
	}

	cfg.Iface = freeL3Iface()
	chooseL3Preset(false).apply(&cfg)
	if tui.Confirm("Fine-Tune The Advanced Settings", false) {
		if !askGREAdvanced(&cfg, side == sideIran) {
			return
		}
	}

	link := ""
	var extras linkExtras
	if side == sideIran {
		extras.restartHours, extras.restartMinute = askScheduledRestart()
		// The kharej's GRE link has to name this server's real address, and
		// nothing on the kharej can work it out: the link carries it.
		link = pendingShareLinkFrom(cfg, greLinkHostFor(cfg), extras)
	}
	summariseL3(cfg, link)
	if extras.restartHours > 0 {
		tui.Info("Scheduled Restart: " + scheduleLabel(extras.restartHours, extras.restartMinute))
	}
	if !tui.Confirm("Create This Tunnel", true) {
		return
	}
	if writeAndStart(cfg.Name, cfg.Render(), cfg.Side, "", link != "") {
		applyLinkExtras(cfg.Name, extras)
		tui.PressEnter()
	}
}

// askGREAdvanced is "fine-tune" for a GRE tunnel. None of it needs answering;
// it reports false when an answer cannot work.
func askGREAdvanced(cfg *l3Spec, iran bool) bool {
	if iran {
		fmt.Println()
		cfg.LocalIP = tui.PromptDefault("This Server's Tunnel Address", cfg.LocalIP)
		cfg.PeerIP = tui.PromptDefault("Kharej Tunnel Address", cfg.PeerIP)
		for l3.CheckTunnelEnds(cfg.LocalIP, cfg.PeerIP) != nil {
			tui.Error("The kharej server's address cannot be this server's own (" + hostOnly(cfg.LocalIP) + ").")
			cfg.PeerIP = tui.PromptDefault("Kharej Tunnel Address", "")
		}
	}

	// The underlay follows the other server's address. A domain can resolve to
	// both, so it is the one case worth offering a choice for; an address of
	// one family has only the one.
	fmt.Println()
	if net.ParseIP(cfg.Addr) == nil {
		six := tui.Confirm("Use IPv6 Between The Two Servers (ip6gre)", cfg.Carrier == l3.CarrierIP6GRE)
		carrier := l3.CarrierGRE
		if six {
			carrier = l3.CarrierIP6GRE
		}
		if _, err := greCarrierForHost(cfg.Addr, carrier); err != nil {
			tui.Error(err.Error())
			tui.PressEnter()
			return false
		}
		cfg.Carrier = carrier
	} else {
		tui.Info("Between The Servers: " + greUnderlayLabel(cfg.Carrier) + " (From The Address)")
	}
	local := strings.TrimSpace(tui.PromptDefault("This Server's Own Address For GRE (Blank = Automatic)", cfg.GRELocal))
	if local != "" {
		ip := net.ParseIP(strings.Trim(local, "[]"))
		if ip == nil || l3.GRECarrierFor(ip) != cfg.Carrier {
			tui.Error("Not an " + greUnderlayLabel(cfg.Carrier) + " address of this server.")
			tui.PressEnter()
			return false
		}
		local = ip.String()
	}
	cfg.GRELocal = local

	fmt.Println()
	cfg.MTU = tui.PromptInt("MTU (0 = From The Interface, Less GRE's Own)", cfg.MTU)
	if !tui.Confirm("Auto MTU (Measure And Correct)", true) {
		off := false
		cfg.AutoMTU = &off
	}
	cfg.Iface = tui.PromptDefault("Interface Name", cfg.Iface)
	if iran {
		fmt.Println()
		tui.Info("GRE Key " + fmt.Sprint(cfg.GREKey) + " Was Picked For This Tunnel — The Setup Link Carries It.")
		cfg.GREKey = askGREKeyDefault(cfg.GREKey)
	}
	fmt.Println()
	cfg.MSSClamp = tui.PromptInt("TCP MSS Clamp (0 = From MTU, -1 = Off)", cfg.MSSClamp)
	if iran && len(cfg.Ports) > 0 {
		fmt.Println()
		cfg.MaxConnections = tui.PromptInt("Max Connections (0 = No Limit)", cfg.MaxConnections)
		cfg.BandwidthMbps = tui.PromptInt("Bandwidth Mbit/s (0 = No Limit)", cfg.BandwidthMbps)
	}
	return true
}

// askGREKeyFrom asks for the key the other server picked, on a side set up by
// hand. There is no default: a key that does not match is a tunnel that drops
// every packet, which looks exactly like one that is blocked.
func askGREKeyFrom(there string) (uint32, bool) {
	fmt.Println()
	for {
		key := tui.PromptInt("GRE Key (From The "+titleWord(there)+" Server)", 0)
		if k := int64(key); k > 0 && k <= math.MaxUint32 {
			return uint32(k), true
		}
		if key == 0 {
			tui.Error("The key is in the " + there + " server's setup link, and under Edit there.")
			tui.PressEnter()
			return 0, false
		}
		tui.Error("The key must be between 1 and 4294967295.")
	}
}

// askGREKeyDefault offers the picked key for changing, keeping it on Enter.
func askGREKeyDefault(current uint32) uint32 {
	for {
		key := tui.PromptInt("GRE Key (Same On Both Ends)", int(current))
		if k := int64(key); k > 0 && k <= math.MaxUint32 {
			return uint32(k)
		}
		tui.Error("The key must be between 1 and 4294967295 — a GRE tunnel here always has one.")
	}
}

// greCarrierForHost is the GRE carrier the other server's address calls for.
// want, when set, is the family asked for; a domain must resolve to it.
func greCarrierForHost(host, want string) (string, error) {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if ip := net.ParseIP(host); ip != nil {
		got := l3.GRECarrierFor(ip)
		if want != "" && want != got {
			return "", fmt.Errorf("%s is an %s address, so the tunnel has to be %s", ip,
				greUnderlayLabel(got), greUnderlayLabel(got))
		}
		if ip.IsUnspecified() || ip.IsLoopback() {
			return "", fmt.Errorf("%s cannot be the other server's address", ip)
		}
		return got, nil
	}
	ips, err := lookupHost(host)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("%s does not resolve: give the other server's IP address", host)
	}
	var have4, have6 bool
	for _, ip := range ips {
		if ip.To4() != nil {
			have4 = true
		} else {
			have6 = true
		}
	}
	switch want {
	case l3.CarrierGRE:
		if !have4 {
			return "", fmt.Errorf("%s has no IPv4 address", host)
		}
		return want, nil
	case l3.CarrierIP6GRE:
		if !have6 {
			return "", fmt.Errorf("%s has no IPv6 address", host)
		}
		return want, nil
	}
	if have4 {
		return l3.CarrierGRE, nil
	}
	return l3.CarrierIP6GRE, nil
}

// lookupHost resolves a name; a variable so a test need not depend on DNS.
var lookupHost = func(host string) ([]net.IP, error) { return net.LookupIP(host) }

// greUnderlayLabel is how the screens name the two families.
func greUnderlayLabel(carrier string) string {
	if carrier == l3.CarrierIP6GRE {
		return "IPv6"
	}
	return "IPv4"
}

// freeGREKey picks a key no GRE tunnel on this server has, so a second tunnel
// to the same kharej — or to any — never collides with the first. Random rather
// than counted, so a tunnel deleted on one end only and set up again later does
// not meet its own leftover on the other. Six digits at most, because a kharej
// set up by hand types it in.
func freeGREKey() uint32 { return freeGREKeyIn(existingL3Configs(), rand.Intn) }

// freeGREKeyIn is freeGREKey against a given set of tunnels and source of
// randomness, so the choice can be tested.
func freeGREKeyIn(existing []config.L3Config, intn func(int) int) uint32 {
	used := map[uint32]bool{}
	for _, l := range existing {
		if l3.KernelCarrier(l.Carrier) {
			used[l.GREKey] = true
		}
	}
	for {
		k := uint32(1000 + intn(999000))
		if !used[k] {
			return k
		}
	}
}

// greTunnelJoining names the GRE tunnel on this server that already joins addr
// with key, or "". The kernel refuses a second; the setup says so first.
func greTunnelJoining(addr string, key uint32) string {
	tunnels := map[string]config.L3Config{}
	for _, t := range List() {
		if cfg, err := LoadTunnelConfig(t.Name); err == nil && cfg.L3.Enabled() {
			tunnels[t.Name] = cfg.L3
		}
	}
	return greJoiningIn(tunnels, addr, key)
}

// greJoiningIn is greTunnelJoining against a given set of tunnels, by name.
func greJoiningIn(tunnels map[string]config.L3Config, addr string, key uint32) string {
	addr = strings.Trim(strings.TrimSpace(addr), "[]")
	for name, l := range tunnels {
		if !l3.KernelCarrier(l.Carrier) {
			continue
		}
		if strings.EqualFold(strings.Trim(l.Addr, "[]"), addr) && l.GREKey == key {
			return name
		}
	}
	return ""
}

// greLinkHost is this server's address as the other end's GRE link must name
// it: the source this server sends from towards the other end, when that is a
// public address; otherwise — behind a 1:1 NAT, say — the public address the
// server is reached at.
func greLinkHost(cfg l3Spec) string {
	if cfg.GRELocal != "" && !privateIP(net.ParseIP(cfg.GRELocal)) {
		return cfg.GRELocal
	}
	if ips, err := lookupHost(cfg.Addr); err == nil {
		for _, remote := range ips {
			if l3.GRECarrierFor(remote) != cfg.Carrier {
				continue
			}
			if local, err := l3.GRELocalToward(remote); err == nil && greSourceSeenAs(local, remote) {
				return local.String()
			}
			break
		}
	}
	public := PublicIPv4()
	if cfg.Carrier == l3.CarrierIP6GRE {
		public = PublicIPv6()
	}
	if public == "-" {
		return ""
	}
	return public
}

// greSourceSeenAs reports whether the other server sees this one's packets
// arriving from local, the source the route towards remote picks.
//
// A public source always is. A private one is too when the other server is
// itself on a private address: the two share that network — a datacenter LAN,
// a private link between two VPSes — and the kharej's link has to name exactly
// the address the packets arrive from. Only a public remote makes a private
// source mean NAT, where the public address is the one the other end sees.
// Refusing the private source there left the setup link with no address at
// all, and the kharej could not be built from it.
func greSourceSeenAs(local, remote net.IP) bool {
	return local != nil && (!privateIP(local) || privateIP(remote))
}

// privateIP reports an address the other server could not reach this one at.
func privateIP(ip net.IP) bool {
	return ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() ||
		// 100.64.0.0/10, the carrier-grade NAT range.
		(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64)
}

// greSummaryRows are the lines a GRE tunnel's summary has in place of the
// carrier socket's.
func greSummaryRows(cfg l3Spec, row func(label, value string)) {
	other := "Kharej"
	if cfg.Side == sideKharej {
		other = "Iran Server"
	}
	row(other, cfg.Addr+"  ("+greUnderlayLabel(cfg.Carrier)+")")
	row("GRE Key", fmt.Sprint(cfg.GREKey))
	row("Encryption", "none — kernel GRE, IP protocol 47")
	if cfg.MTU > 0 {
		row("MTU", fmt.Sprint(cfg.MTU))
	} else {
		row("MTU", fmt.Sprintf("from the interface, less %d", l3.GREOverhead(cfg.Carrier, cfg.GREKey)))
	}
}

// greKharejRow is the line the link-built kharej's summary shows for where the
// tunnel goes.
func greKharejRow(form PeerForm) (string, string) {
	return "Iran Server", form.ServerAddr + "  (" + greUnderlayLabel(form.Carrier) + ", key " + fmt.Sprint(form.GREKey) + ")"
}

// greSpec is NewDirectTunnel.spec for a GRE tunnel: what the panel, a setup
// link and a managed node all build one from. It refuses what the wizard
// refuses, in the same words.
func (n NewDirectTunnel) greSpec(side directSide, carrier string) (l3Spec, error) {
	host := strings.Trim(strings.TrimSpace(n.PeerAddr), "[]")
	if host == "" {
		if side == sideIran {
			return l3Spec{}, fmt.Errorf("the kharej server's address is required on the Iran side")
		}
		return l3Spec{}, fmt.Errorf("a GRE tunnel's kharej side needs the Iran server's real address: " +
			"a GRE link names both servers")
	}
	// "gre" is the screens' one entry, whose family follows the address;
	// "ip6gre" — from a setup link, or a file — asks for IPv6 by name.
	want := ""
	if carrier == l3.CarrierIP6GRE {
		want = carrier
	}
	carrier, err := greCarrierForHost(host, want)
	if err != nil {
		return l3Spec{}, err
	}

	name := strings.TrimSpace(n.Name)
	if name == "" {
		name = freeName("gre-" + side.String())
	}
	if !validName(name) {
		return l3Spec{}, fmt.Errorf("tunnel name %q may use only letters, digits, dots and dashes", name)
	}
	name = uniqueName(name)

	key := n.GREKey
	if key == 0 {
		if side == sideKharej {
			return l3Spec{}, fmt.Errorf("the GRE key is required on the kharej side, and must be the Iran " +
				"server's: it is in its setup link, and under Edit there")
		}
		key = freeGREKey()
	}
	if other := greTunnelJoining(host, key); other != "" {
		return l3Spec{}, fmt.Errorf("tunnel %s on this server already joins %s with GRE key %d, and the "+
			"kernel allows one per key — give this one another key", other, host, key)
	}

	local := ""
	if raw := strings.Trim(strings.TrimSpace(n.GRELocal), "[]"); raw != "" {
		ip := net.ParseIP(raw)
		if ip == nil || l3.GRECarrierFor(ip) != carrier {
			return l3Spec{}, fmt.Errorf("%q is not an %s address of this server", n.GRELocal, greUnderlayLabel(carrier))
		}
		local = ip.String()
	}
	if n.FEC || n.FECData > 0 || n.FECParity > 0 {
		return l3Spec{}, fmt.Errorf("a GRE tunnel has no error correction: the kernel sends it, " +
			"and FEC works only on Backpack's own carriers")
	}

	localIP, peerIP := strings.TrimSpace(n.LocalIP), strings.TrimSpace(n.PeerIP)
	if localIP == "" || peerIP == "" {
		localIP, peerIP = freeL3Subnet(side)
	}
	if err := l3.CheckTunnelEnds(localIP, peerIP); err != nil {
		return l3Spec{}, err
	}
	if strings.TrimSpace(n.LocalIP) != "" {
		if other := l3BlockOwner(localIP); other != "" {
			return l3Spec{}, fmt.Errorf("tunnel %s on this server already uses %sx, the block given for "+
				"this one: pick a free block on the %s server and set up both ends again",
				other, l3Block(localIP), map[directSide]string{sideIran: "kharej", sideKharej: "Iran"}[side])
		}
	}

	spec := l3Spec{
		Name: name, Side: side, Carrier: carrier, Encap: "gre", GREKey: key,
		Addr: host, GRELocal: local,
		Iface: freeL3Iface(), LocalIP: localIP, PeerIP: peerIP,
		MTU:            n.MTU,
		MSSClamp:       n.MSSClamp,
		AutoMTU:        n.AutoMTU,
		MaxConnections: n.MaxConnections,
		BandwidthMbps:  n.BandwidthMbps,
	}
	findL3Preset(strings.ToLower(strings.TrimSpace(n.Preset))).apply(&spec)
	if side == sideIran {
		spec.Ports = parsePorts(n.Ports)
		if len(spec.Ports) == 0 {
			return l3Spec{}, fmt.Errorf("at least one forwarded port is required on the Iran side")
		}
		if err := validatePortSpecs(spec.Ports); err != nil {
			return l3Spec{}, err
		}
		spec.AcceptUDP = n.AcceptUDP
	}
	return spec, nil
}

// greShareHost is the address a GRE tunnel's setup link gives the other end:
// the one asked for when it can be this server's GRE endpoint, otherwise the one
// the link would be created from. A domain is kept as given — it is how the
// operator reaches this server, and it is resolved at the other end's start.
func greShareHost(given string, cfg l3Spec) string {
	given = strings.Trim(strings.TrimSpace(given), "[]")
	// With no other server named there is no route to find a source on, and
	// what was given is the best there is.
	if strings.TrimSpace(cfg.Addr) == "" {
		return given
	}
	if given != "" {
		ip := net.ParseIP(given)
		if ip == nil {
			return given
		}
		if l3.GRECarrierFor(ip) == cfg.Carrier && greSourceSeenAs(ip, net.ParseIP(strings.Trim(cfg.Addr, "[]"))) {
			return ip.String()
		}
	}
	return greLinkHostFor(cfg)
}

// greLinkHostFor is greLinkHost; a variable so a test of a link need not ask
// the network what this machine's public address is.
var greLinkHostFor = greLinkHost

// startGRECase fills a Connection Test's GRE case on the Iran side: the same
// tunnel the wizard builds — its key, its MTU taken from the interface, the
// preset asked for — between the two servers' own addresses, over IPv6 when
// that is what the kharej reaches this server on. The kharej's address is not
// known until it joins; Run fills it in.
func (s *ConnTestIran) startGRECase(c *connTestCase, i int, host, preset string) error {
	carrier := l3.CarrierGRE
	if ctFamily(host) == "IPv6" {
		carrier = l3.CarrierIP6GRE
	}
	block := ctFreeL3Block(i)
	c.peerIP = block + "2"
	c.l3 = l3Spec{
		Name: c.name, Side: sideIran, Carrier: carrier, Encap: "gre",
		// Random, so a test never meets a GRE tunnel that already joins the
		// two servers; such a tunnel holds a key of its own.
		GREKey: uint32(1000 + rand.Intn(999000)),
		Iface:  fmt.Sprintf("bpt%d", i), LocalIP: block + "1/30", PeerIP: block + "2",
	}
	findL3Preset(preset).apply(&c.l3)
	raw := pendingShareLinkFrom(c.l3, host, linkExtras{})
	link, err := DecodeShareLink(raw)
	if err != nil {
		return fmt.Errorf("building the gre test link: %w", err)
	}
	// The kharej's link names this server at the address it reaches the
	// coordinator on, which is the one the test is about.
	link.Host, link.Name = strings.Trim(host, "[]"), c.name
	c.link = link
	return nil
}
