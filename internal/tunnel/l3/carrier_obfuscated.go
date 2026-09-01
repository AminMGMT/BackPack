package l3

import (
	"fmt"
	"net"

	"github.com/backpack/backpack/internal/utils/network"
)

// The obfuscated carriers.
//
// Plain UDP is the right carrier on a path that does not interfere. On one
// that does, it is the first thing to go: a long-lived UDP flow to a foreign
// address is among the easiest patterns to rate-limit or drop. These three
// carry the same sealed datagrams somewhere less obvious.
//
//	pck    raw TCP segments this process builds itself — no socket, no
//	       handshake, no connection state for netfilter to interfere with,
//	       but everything a capture sees is an ordinary TCP flow
//	xdi    ICMP echo, for a path that filters UDP and TCP but not ping
//	spoof  raw IP with a forged source address
//
// Each is an adapter over a constructor that already exists and is already in
// production under the KCP transports. The carriers themselves are untouched:
// what changes is only that the layer-3 tunnel's own sealed datagrams go
// through them instead of KCP's.
//
// All three are Linux-only and need CAP_NET_RAW, which the transports that use
// them already document.

// obfuscatedCarrier wraps a bare net.PacketConn from the network package with
// the overhead and name the tunnel needs.
type obfuscatedCarrier struct {
	net.PacketConn
	overhead int
	name     string
	// note is what the carrier wants said about itself at startup — currently
	// only whether the XDP receive fast path attached or fell back. It is
	// carried here because the carrier's own type is hidden behind
	// net.PacketConn once it is wrapped, so the method would not be promoted.
	note string
}

func (c *obfuscatedCarrier) Overhead() int       { return c.overhead }
func (c *obfuscatedCarrier) CarrierName() string { return c.name }

// Diag returns the startup note, or "" when there is nothing to say.
//
// It exists because the XDP fast path declines silently by design: a kernel too
// old, a driver without XDP, or a verifier rejection falls back to the ordinary
// receive rather than failing, and an operator who turned it on has no other
// way to learn which happened. Computing that and dropping it — which is what
// happened while nothing called this — is the same as not computing it.
func (c *obfuscatedCarrier) Diag() string { return c.note }

// openPck builds the packet-level TCP carrier.
func openPck(cfg Config) (DatagramCarrier, net.Addr, error) {
	conn, peer, err := network.NewPckPacketConn(
		cfg.Mode == ModeListen, cfg.Token, cfg.Addr, cfg.Pck)
	if err != nil {
		return nil, nil, err
	}
	return &obfuscatedCarrier{
		PacketConn: conn,
		overhead:   network.PckOverhead(),
		name:       "pck",
	}, peer, nil
}

// openXdi builds the ICMP-echo carrier.
func openXdi(cfg Config) (DatagramCarrier, net.Addr, error) {
	conn, peer, err := network.NewXdiPacketConn(
		cfg.Mode == ModeListen, cfg.Token, cfg.Addr)
	if err != nil {
		return nil, nil, err
	}
	return &obfuscatedCarrier{
		PacketConn: conn,
		overhead:   network.XdiOverhead(),
		name:       "xdi",
	}, peer, nil
}

// openSpoof builds the forged-source carrier.
//
// It needs the peer's real address — the address packets are actually routed
// to, as opposed to the forged one written into the header. The dialling side
// takes it from the address it was told to reach; the listening side cannot
// infer it, because its peer forges the source of every packet it sends, so it
// has to be configured.
func openSpoof(cfg Config) (DatagramCarrier, net.Addr, error) {
	uplink, downlink := network.ResolveSpoofDirections(
		cfg.Spoof.SpoofProfile, cfg.Spoof.SpoofUplink, cfg.Spoof.SpoofDownlink)

	realPeer, err := spoofRealPeer(cfg)
	if err != nil {
		return nil, nil, err
	}

	carrier := network.SpoofCarrier{
		Uplink: uplink, Downlink: downlink,
		SrcIP:      cfg.Spoof.SpoofSrcIP,
		SrcPool:    cfg.Spoof.SpoofSrcPool,
		PeerIP:     realPeer.String(),
		Interface:  cfg.Spoof.SpoofInterface,
		XDPIface:   cfg.Spoof.SpoofXDPInterface,
		SockBuf:    cfg.SockBuf,
		PeerSrcIP:  cfg.Spoof.SpoofPeerSrcIP,
		ReplySplit: cfg.Spoof.SpoofICMPReply,
		MTU:        cfg.Spoof.SpoofMTU,
		DPI:        network.SpoofDPIFromConfig(cfg.Spoof),
	}

	conn, err := network.NewSpoofPacketConn(
		cfg.Mode == ModeListen, cfg.Token, carrier, realPeer)
	if err != nil {
		return nil, nil, err
	}

	// The uplink and downlink profiles can differ, and the MTU has to hold for
	// both, so the more expensive of the two is what the budget is built on.
	overhead := network.SpoofOverhead(uplink)
	if down := network.SpoofOverhead(downlink); down > overhead {
		overhead = down
	}
	// Padding is added inside the carrier, after the tunnel has sized its
	// packet, so its worst case has to come out of the same budget.
	if cfg.Spoof.SpoofPadding {
		pad := cfg.Spoof.SpoofPaddingMax
		if pad <= 0 {
			pad = 255
		}
		overhead += pad
	}

	// The peer address is the real routing destination for both ends: the
	// listening side is told it, rather than learning it, precisely because
	// the source addresses it sees are forged.
	return &obfuscatedCarrier{
		PacketConn: conn,
		overhead:   overhead,
		name:       "spoof",
		note:       network.SpoofDiag(conn),
	}, &net.IPAddr{IP: realPeer}, nil
}

// spoofRealPeer works out where the forged packets are actually routed.
func spoofRealPeer(cfg Config) (net.IP, error) {
	if configured := cfg.Spoof.SpoofPeerIP; configured != "" {
		ip := net.ParseIP(configured)
		if ip == nil {
			return nil, fmt.Errorf("l3: spoof_peer_ip %q is not a valid IP address", configured)
		}
		return ip, nil
	}
	if cfg.Mode == ModeListen {
		return nil, fmt.Errorf(
			"l3: the spoof carrier needs spoof_peer_ip when listening, " +
				"because the peer forges the source of every packet it sends")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("l3: reading the peer address from %q: %w", cfg.Addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		resolved, err := net.ResolveIPAddr("ip", host)
		if err != nil {
			return nil, fmt.Errorf("l3: resolving the spoof peer %q: %w", host, err)
		}
		ip = resolved.IP
	}
	return ip, nil
}
