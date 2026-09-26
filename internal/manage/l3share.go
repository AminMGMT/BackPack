package manage

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/tunnel/portmap"
)

// Several kharej servers behind one Iran server.
//
// On a direct layer-3 tunnel the Iran side dials, so an Iran server with more
// than one kharej simply runs one tunnel per kharej: its own interface, its own
// 10.10.N.0/30, its own carrier socket. The kernel routes each peer address
// over its own interface, so a forwarded port on any one of those tunnels can
// reach every kharej — which is what lets a port be spread over all of them
// ("443=10.10.0.2:443|10.10.1.2:443", see backends.go in the l3 package).
//
// What stood in the way was the wizard. Setting up the second kharej and
// asking for the same ports as the first gave a tunnel whose forwarder could
// not bind them, because the first one already had. The operator wanted the
// port served by both kharej and got one tunnel refusing to start its
// listeners. Now the wizard notices the overlap and offers the thing that was
// meant: the port stays on the tunnel that already has it, and the new kharej
// is added to it as another backend.

// l3Tunnel is one layer-3 tunnel configured on this machine.
type l3Tunnel struct {
	T Tunnel
	L config.L3Config
}

// l3Share is one forwarded port that an existing tunnel serves and that the new
// tunnel's kharej should be added to.
type l3Share struct {
	Tunnel  l3Tunnel
	OldSpec string // the mapping as the existing tunnel has it
	NewSpec string // the same mapping with the new kharej added
}

// eachIranL3Tunnel visits every layer-3 tunnel on this machine that dials out,
// which is what an Iran server's direct tunnels are.
func eachIranL3Tunnel(visit func(l3Tunnel)) {
	for _, t := range List() {
		cfg, err := LoadTunnelConfig(t.Name)
		if err != nil || !cfg.L3.Enabled() {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(cfg.L3.Mode), "listen") {
			continue
		}
		visit(l3Tunnel{T: t, L: cfg.L3})
	}
}

// planL3Sharing sorts the new tunnel's port specs into those it can keep, those
// another tunnel already serves and can share with the new kharej, and those
// that overlap in a way that cannot be shared (a port range: which backend
// would port 10005 belong to?).
//
// Pure, so the decision can be tested without a terminal or a config directory.
func planL3Sharing(specs []string, newPeer string, existing []l3Tunnel) (keep []string, shares []l3Share, clash []string) {
	peer := hostOnly(newPeer)
	// What each existing spec listens on, and whether it is a single port.
	type owner struct {
		tunnel l3Tunnel
		spec   string
		single portmap.Mapping
		isOne  bool
	}
	listens := map[string][]owner{}
	for _, e := range existing {
		for _, spec := range e.L.Ports {
			ms, err := portmap.Expand([]string{spec}, hostOnly(e.L.PeerIP))
			if err != nil {
				continue
			}
			for _, m := range ms {
				o := owner{tunnel: e, spec: spec, isOne: len(ms) == 1}
				if o.isOne {
					o.single = m
				}
				listens[m.Listen] = append(listens[m.Listen], o)
			}
		}
	}

	// A spec already rewritten once, so a second new port on the same
	// existing spec extends the rewrite instead of starting from the original.
	at := map[string]int{}

	for _, spec := range specs {
		ms, err := portmap.Expand([]string{spec}, peer)
		if err != nil {
			keep = append(keep, spec) // validated before; left to the usual error
			continue
		}
		var owners []owner
		for _, m := range ms {
			owners = append(owners, listens[m.Listen]...)
		}
		if len(owners) == 0 {
			keep = append(keep, spec)
			continue
		}
		if len(ms) != 1 || len(owners) != 1 || !owners[0].isOne {
			clash = append(clash, spec)
			continue
		}

		o := owners[0]
		key := o.tunnel.T.Name + "\x00" + o.spec
		i, seen := at[key]
		if !seen {
			shares = append(shares, l3Share{Tunnel: o.tunnel, OldSpec: o.spec,
				NewSpec: joinL3Spec(o.spec, o.single.Targets)})
			i = len(shares) - 1
			at[key] = i
		}
		_, right, _ := strings.Cut(shares[i].NewSpec, "=")
		targets := strings.Split(right, "|")
		for _, t := range ms[0].Targets {
			if !containsString(targets, t) {
				targets = append(targets, t)
			}
		}
		shares[i].NewSpec = joinL3Spec(o.spec, targets)
	}
	return keep, shares, clash
}

// joinL3Spec renders a mapping's listen side with an explicit list of backends.
// The listen side is kept exactly as the operator wrote it.
func joinL3Spec(spec string, targets []string) string {
	listen, _, _ := strings.Cut(spec, "=")
	return strings.TrimSpace(listen) + "=" + strings.Join(targets, "|")
}

func hostOnly(addr string) string {
	addr, _, _ = strings.Cut(strings.TrimSpace(addr), "/")
	return addr
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// offerL3Sharing runs the plan against this machine's tunnels, asks, and
// applies it. It returns the ports the new tunnel should keep for itself.
func offerL3Sharing(cfg l3Spec) []string {
	var existing []l3Tunnel
	eachIranL3Tunnel(func(t l3Tunnel) { existing = append(existing, t) })
	keep, shares, clash := planL3Sharing(cfg.Ports, cfg.PeerIP, existing)

	if len(clash) > 0 {
		fmt.Println()
		tui.Warn("These ports overlap ports another tunnel here already forwards, as a")
		tui.Warn("range, which cannot be shared between kharej servers. They are left out:")
		tui.Warn("  " + strings.Join(clash, ", "))
	}
	if len(shares) == 0 {
		return keep
	}

	fmt.Println()
	tui.Info("Some of these ports are already forwarded by another tunnel on this")
	tui.Info("server. They can be spread over both kharej servers instead: each new")
	tui.Info("connection goes to the kharej carrying the fewest, and one that stops")
	tui.Info("answering is skipped.")
	for _, s := range shares {
		tui.Info(fmt.Sprintf("  %s (tunnel %s)  →  %s", s.OldSpec, s.Tunnel.T.Name, s.NewSpec))
	}
	if !tui.Confirm("Share these ports across the kharej servers", true) {
		tui.Warn("Left out of this tunnel, since the other one already holds them.")
		return keep
	}

	// Grouped per tunnel, so each is rewritten and restarted once.
	for _, g := range groupShares(shares) {
		name, l := g.tunnel.T.Name, g.tunnel.L
		l.Ports = g.ports
		// A shared port carries whatever the tunnel holding it carries. Asked
		// for UDP here, the new kharej would otherwise get TCP only on it.
		if cfg.AcceptUDP && !l.AcceptUDP {
			l.AcceptUDP = true
			tui.Info("UDP forwarding turned on for tunnel " + name + ", as asked for here.")
		}
		if err := writeL3AndRestart(g.tunnel.T, l); err != nil {
			tui.Error("Could not update tunnel " + name + ": " + err.Error())
			continue
		}
		tui.Success("Tunnel " + name + " now shares its ports with this kharej.")
	}
	return keep
}

// writeL3AndRestart saves a layer-3 config and restarts its service, quietly:
// it runs in the middle of the setup wizard, which has its own screens.
func writeL3AndRestart(t Tunnel, l config.L3Config) error {
	body := l3SpecOf(t, l).render()
	if err := validateRendered(body); err != nil {
		return err
	}
	if err := app.WriteFileAtomic(app.ConfigPath(t.Name), []byte(body), app.TunnelConfigMode); err != nil {
		return err
	}
	return RestartService(t.Service)
}

// shareL3PortsQuietly is offerL3Sharing without the questions, for the panel:
// ports another tunnel here already forwards are shared with the new kharej,
// and an overlap that cannot be shared is an error rather than a tunnel whose
// listener fails to bind.
func shareL3PortsQuietly(cfg l3Spec) ([]string, error) {
	var existing []l3Tunnel
	eachIranL3Tunnel(func(t l3Tunnel) { existing = append(existing, t) })
	keep, shares, clash := planL3Sharing(cfg.Ports, cfg.PeerIP, existing)
	if len(clash) > 0 {
		return nil, fmt.Errorf("ports %s overlap a port range another tunnel on this server "+
			"already forwards, and a range cannot be shared between kharej servers — "+
			"choose other ports", strings.Join(clash, ", "))
	}
	for _, t := range groupShares(shares) {
		l := t.tunnel.L
		l.Ports = t.ports
		if cfg.AcceptUDP {
			l.AcceptUDP = true
		}
		if err := writeL3AndRestart(t.tunnel.T, l); err != nil {
			return nil, fmt.Errorf("could not share ports with tunnel %s: %w", t.tunnel.T.Name, err)
		}
	}
	return keep, nil
}

// sharedTunnel is one existing tunnel with its ports after sharing.
type sharedTunnel struct {
	tunnel l3Tunnel
	ports  []string
}

// groupShares applies the rewrites to each affected tunnel's port list, one
// entry per tunnel, in the order they were first named.
func groupShares(shares []l3Share) []sharedTunnel {
	var out []sharedTunnel
	at := map[string]int{}
	for _, s := range shares {
		i, seen := at[s.Tunnel.T.Name]
		if !seen {
			ports := make([]string, len(s.Tunnel.L.Ports))
			copy(ports, s.Tunnel.L.Ports)
			out = append(out, sharedTunnel{tunnel: s.Tunnel, ports: ports})
			i = len(out) - 1
			at[s.Tunnel.T.Name] = i
		}
		for j, p := range out[i].ports {
			if p == s.OldSpec {
				out[i].ports[j] = s.NewSpec
			}
		}
	}
	return out
}

// busyForwardPorts lists the mapped ports something on this machine already
// listens on. The web panel's own port (7777 by default) is the usual one: a
// tunnel forwarding it started, reported nothing wrong in the wizard, and its
// forwarder failed to bind in a log nobody was reading.
func busyForwardPorts(specs []string, peer string) []string {
	ms, err := portmap.Expand(specs, hostOnly(peer))
	if err != nil {
		return nil
	}
	var busy []string
	for _, m := range ms {
		if portHeld(m.Listen) {
			busy = append(busy, strings.TrimPrefix(m.Listen, ":"))
		}
	}
	return busy
}

// portHeld reports whether something already listens on addr. Only "address
// in use" counts: a refusal for any other reason — a privileged port asked for
// without root, an address not on this machine yet — says nothing about
// whether the port is taken, and is not this check's to report.
func portHeld(addr string) bool {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.Is(err, syscall.EADDRINUSE)
	}
	ln.Close()
	return false
}

// udpCarriers are the carriers that bind a UDP socket of their own on the
// listening side, so two tunnels with overlapping ports cannot both run.
var udpCarriers = map[string]bool{"udp": true, "quic": true}

// l3PortRange is the UDP ports a listening tunnel binds: its port, and the
// ones after it when the udp carrier is spread over several sockets.
func l3PortRange(port, paths int) (lo, hi int) {
	if paths < 1 {
		paths = 1
	}
	return port, port + paths - 1
}

// l3ListenClash says why a new kharej tunnel's UDP port cannot work here, or
// "" when it can. Two Iran servers set up with the wizard's default port both
// hand their kharej 9000; the second tunnel then fails to bind and restarts for
// ever, with the reason only in its journal. Pure, so it can be tested.
func l3ListenClash(name, carrier string, port, paths int, existing []l3Tunnel) string {
	if !udpCarriers[carrier] {
		return ""
	}
	lo, hi := l3PortRange(port, paths)
	for _, e := range existing {
		if strings.EqualFold(e.T.Name, name) || !udpCarriers[orDefault(e.L.Carrier, "udp")] {
			continue
		}
		_, p, err := net.SplitHostPort(e.L.Addr)
		if err != nil {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			continue
		}
		elo, ehi := l3PortRange(n, e.L.Paths)
		if lo <= ehi && elo <= hi {
			return fmt.Sprintf("tunnel %q already listens on UDP %s here. Two tunnels cannot share it: "+
				"the second fails to bind. Give this tunnel another port — on the Iran server too, "+
				"since it dials this one.", e.T.Name, portSpan(elo, ehi))
		}
	}
	return ""
}

func portSpan(lo, hi int) string {
	if lo == hi {
		return strconv.Itoa(lo)
	}
	return fmt.Sprintf("%d-%d", lo, hi)
}

// kharejL3Tunnels is every layer-3 tunnel on this machine that listens.
func kharejL3Tunnels() []l3Tunnel {
	var out []l3Tunnel
	for _, t := range List() {
		cfg, err := LoadTunnelConfig(t.Name)
		if err != nil || !cfg.L3.Enabled() || !strings.EqualFold(strings.TrimSpace(cfg.L3.Mode), "listen") {
			continue
		}
		out = append(out, l3Tunnel{T: t, L: cfg.L3})
	}
	return out
}

// kharejPortClash is l3ListenClash for a spec about to be written.
func kharejPortClash(s l3Spec) string {
	if s.Side != sideKharej {
		return ""
	}
	_, p, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return ""
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return ""
	}
	return l3ListenClash(s.Name, s.Carrier, n, s.Paths, kharejL3Tunnels())
}
