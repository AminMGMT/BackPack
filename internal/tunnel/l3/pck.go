package l3

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/backpack/backpack/internal/tunnel/paqet"
)

// PCK.
//
// Since v2.0.0 carrier = "pck" is not a datagram carrier for the layer-3
// engine. It is paqet's engine (internal/tunnel/paqet): TCP segments crafted
// and captured with libpcap, KCP and smux above them, forwarding the configured
// ports. There is no TUN device and no session of this package's own, so like
// the kernel GRE carriers it takes a path of its own through Run, and the
// engine around it — the forwarder, the metrics, Up() and Stats(), the
// watchdog's peer — is shared.
//
// "sni" is unchanged. It still opens the raw-datagram pck carrier
// (carrier_obfuscated.go, internal/utils/network) with a ClientHello in front,
// and that code stays for it alone.
//
// # Which end is which
//
// The end that dials (mode = "dial", the Iran side) is paqet's client: it
// listens on the forwarded ports and sends each connection through. The end that
// listens (the kharej side) is paqet's server: it dials the target the stream
// names. A file for the previous PCK maps one-to-one, because that engine had
// the same two ends and the same ports on the Iran side.
//
// # What a target meant, and means now
//
// A mapping "443=8443" reached peer_ip:8443 — the other end's address on the
// TUN, which the kharej kernel delivered to itself, so it hit whatever listened
// on 8443 there (a wildcard bind, since nothing but the tunnel owned that
// address). The same mapping now dials 127.0.0.1:8443 on the kharej side, which
// reaches the same service, and one more kind: a service bound to loopback. A
// target that names peer_ip explicitly is read the same way. A target that
// names any other machine is refused by the kharej server unless its own file
// sets pck_remote_targets = true — before, reaching one needed forwarding and
// NAT set up there, so the kharej's operator opted in then as well; see
// paqet's targets.go for why the decision is that server's.

// # Migrating an existing pck tunnel
//
// A file written by v1.9.0 or before — carrier = "pck", the TUN keys, a preset
// label, a sockbuf — runs on this engine as it stands. It is READ compatibly,
// every time, and never rewritten. The choice, and why:
//
//   - A rewrite at update time (the way migrate.go corrects a permission or
//     drops a dead key) would have to decide what to do with iface, local_ip,
//     peer_ip, mtu, txqueuelen, qdisc, auto_mtu, mss_clamp, fec_* and encap,
//     none of which mean anything now. Dropping them makes the file unusable
//     by the previous binary, which is exactly what a rollback restores (the
//     backup snapshot holds the old binary and the old configs together). A
//     file left alone rolls back to a working tunnel; a rewritten one does not.
//   - The keys the engine still reads keep their meaning: mode, addr, token,
//     ports, accept_udp, max_connections, bandwidth_mbps, preset, sockbuf,
//     pck_interface, pck_gateway_mac, pck_flags. The rest are inert, and
//     PckNotes says so in the log, once, for the ones an operator might think
//     are doing something.
//   - The parameters the preset implied are not stored in the old file —
//     "preset" there is a label next to txqueuelen and sockbuf — so they are
//     derived from the label at run time (paqet.PresetParams). A file that
//     names a preset keeps getting that preset's engine tuning, and the
//     explicit pck_* keys override it for the operators who want to.
//   - Nothing about the on-disk format is a promise: the setup and the editor
//     write whatever the current version wants, and may stop writing the dead
//     keys. Reading them stays.
//
// What does not carry over, and is said rather than hidden: a tunnel with no
// ports (it carried routed traffic over the TUN, which no longer exists for pck;
// PckNotes names the carriers that still do), ports on the listening side
// (ignored on that side, with a note in the log; refusing the file would stop a migrated tunnel from starting at all), and a target that reached a machine
// other than the far server through its kernel's forwarding (it is now dialled
// from the far server directly, once its file sets pck_remote_targets = true).
//
// The other end must be updated too: the wire is different, and an old peer is
// named in the log of whichever end is new (paqet.looksLikeOldPck, and the
// "no answer" warning for the end that cannot see the peer at all).

// loopbackTarget is where a target with no host of its own, or one naming
// peer_ip, is dialled on the far side.
const loopbackTarget = "127.0.0.1"

// PckEngineConfig renders the l3 configuration as the engine's.
func PckEngineConfig(c Config) (paqet.Config, error) {
	p, ok := paqet.PresetParams(c.Preset)
	if !ok {
		// An unknown or empty name is Turbo, the default; a label that is not
		// one of ours is not an error for a carrier that only labels.
		p, _ = paqet.PresetParams(paqet.PresetTurbo)
	}
	over := func(dst *int, v int) {
		if v != 0 {
			*dst = v
		}
	}
	over(&p.Conns, c.PckConns)
	over(&p.MTU, c.PckKCPMTU)
	over(&p.SndWnd, c.PckKCPSndWnd)
	over(&p.RcvWnd, c.PckKCPRcvWnd)
	// sockbuf is the capture ring: the same memory the preset set for the old
	// carrier's sockets.
	over(&p.PcapBuf, c.SockBuf)
	if c.PckKCPMode != "" {
		p.Mode = strings.ToLower(strings.TrimSpace(c.PckKCPMode))
	}
	if c.PckKCPBlock != "" {
		p.Block = strings.ToLower(strings.TrimSpace(c.PckKCPBlock))
	}
	p = p.Resolve(c.Preset)
	return paqet.Config{
		Server:     c.Mode == ModeListen,
		Addr:       c.Addr,
		Token:      c.Token,
		Interface:  c.Pck.Interface,
		GatewayMAC: c.Pck.GatewayMAC,
		Flags:      c.Pck.Flags,
		Params:     p,
		// Read on the listening side only: the side that dials the targets.
		RemoteTargets: c.PckRemoteTargets,
	}, nil
}

// validatePck is Validate for carrier = "pck": the same file, judged by what
// the engine actually reads. Keys it ignores are not errors, so a file written
// for the previous PCK is accepted as it stands.
func (c *Config) validatePck() error {
	pc, err := PckEngineConfig(*c)
	if err != nil {
		return err
	}
	if err := pc.Validate(); err != nil {
		return fmt.Errorf("l3: pck: %w", err)
	}
	if c.Mode == ModeDial {
		host, _, _ := net.SplitHostPort(c.Addr)
		if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
			return fmt.Errorf("l3: pck: addr %q is an IPv6 address; pck carries IPv4 only", c.Addr)
		}
	}
	return nil
}

// PckNotes lists, for the log at start, what in a pck tunnel's file the engine
// does not use. A file from before the engine changed carries keys for a TUN
// device; they are harmless, and saying so once beats an operator wondering
// whether the MTU they set is doing anything.
func PckNotes(c Config) []string {
	var notes []string
	if c.FEC.Data != 0 || c.FEC.Parity != 0 {
		notes = append(notes, "fec_data and fec_parity are ignored: KCP retransmits lost packets itself")
	}
	if c.Mode == ModeListen && len(c.Ports) > 0 {
		notes = append(notes, "ports are served by the dialling side (Iran) of a pck tunnel — the kharej side dials the "+
			"targets — so the ports in this file are ignored here. Put them in the Iran server's file")
	}
	if c.Mode == ModeDial && len(c.Ports) == 0 {
		notes = append(notes, "this tunnel forwards no ports, and pck carries ports only: nothing will cross it. "+
			"The previous PCK also carried routed traffic over a TUN device; for that, use the xdi, sni or spoof carrier")
	}
	return notes
}

// pckTarget is the forwarder's dial address as the far side is asked for it.
func pckTarget(c Config, addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	peer, _, _ := strings.Cut(strings.TrimSpace(c.PeerIP), "/")
	if host == "" || (peer != "" && host == peer) {
		host = loopbackTarget
	}
	return net.JoinHostPort(host, port)
}

// UsesStreams reports whether this tunnel serves its forwarded ports through
// streams of its own engine rather than by dialling over a TUN device. The
// forwarder needs to know which dial to use.
func (t *Tunnel) UsesStreams() bool { return t.pck != nil }

// StreamDialer is the dial the forwarder uses when UsesStreams: a connection
// to addr, made by the other server.
func (t *Tunnel) StreamDialer() DialFunc {
	return func(ctx context.Context, network, addr string, report func(error)) (net.Conn, error) {
		return t.pck.Dial(ctx, network, pckTarget(t.cfg, addr), report)
	}
}

// runPaqet is one generation of a pck tunnel.
func (t *Tunnel) runPaqet(ctx context.Context) error {
	pc := t.pck.Config()
	for _, note := range PckNotes(t.cfg) {
		t.log.Warnf("l3: pck: %s", note)
	}
	t.log.Infof("l3: pck (paqet engine) %s, preset %q: %d KCP session(s), mode %s, mtu %d, window ceiling %d/%d, cipher %s",
		map[bool]string{true: "listening", false: "dialling"}[pc.Server], t.cfg.Preset,
		pc.Params.Conns, pc.Params.Mode, pc.Params.MTU, pc.Params.SndWnd, pc.Params.RcvWnd, pc.Params.Block)
	return t.pck.Run(ctx)
}
