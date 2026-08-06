//go:build linux

package network

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/ipv4"
)

// A net.PacketConn that carries the tunnel's KCP datagrams inside raw IPv4
// packets whose source address is forged — the "IP Spoofing" transport.
//
// It is the same shape as the xdi ICMP carrier: KCP, its forward error
// correction and its encryption all sit on top unchanged, because to them this
// is just a net.PacketConn like the UDP socket they usually get. What it does
// underneath is hand-build each outgoing IP packet — choosing its source
// address rather than letting the kernel stamp the real one — and wrap the
// datagram in a UDP or TCP shim so it reads on the wire as ordinary L4 traffic.
// Incoming packets are demultiplexed by the same token-derived tag and
// direction byte the xdi frame uses, so several tunnels on one host never touch
// each other's packets and a stray datagram is dropped.
//
// The one thing to understand about the return path: routing always uses the
// REAL destination — the address KCP hands WriteTo, which is the real peer — so
// the packet genuinely arrives. Only the SOURCE in the header is replaced. That
// means the far end sees the forged source and, unless the network routes that
// forged source back (no BCP38 egress filtering on the path, or a source the
// operator actually controls), its replies will not return. Whether they do is
// a property of the route, not of this code, and is the thing to prove before
// relying on it.
//
// It is experimental, Linux only, and costs a raw socket (root or CAP_NET_RAW).

// spoofConn is one tunnel's raw-IP carrier. One is opened per tunnel; several on
// one host coexist because each reads only packets carrying its own tag.
type spoofConn struct {
	raw   *ipv4.RawConn  // read and write of raw IPv4 packets, with deadlines
	pc    net.PacketConn // kept for LocalAddr/Close
	proto int            // IANA protocol number of the L4 shim (6 or 17)

	profile SpoofProfile
	server  bool
	tag     [xdiTagLen]byte
	port    uint16 // stable per-tunnel L4 port stamped into the shim

	spoofSrc net.IP // forged source, or nil to use the real local address

	ipID   atomic.Uint32 // IP identification field, incremented per packet
	tcpSeq atomic.Uint32 // TCP sequence number, for the tcp profile

	srcCache sync.Map // realDst string -> net.IP, the local source toward it
}

// spoofOverhead is what the carrier's framing costs on top of the payload, so
// the KCP layer can be told a small enough MTU that a datagram never fragments:
// the IP header, the L4 shim, and this tunnel's tag-and-direction prefix.
func spoofOverhead(p SpoofProfile) int {
	l4 := 8 // UDP header
	if p == SpoofProfileTCP {
		l4 = 20 // TCP header, no options
	}
	return ipv4.HeaderLen + l4 + xdiHeaderLen
}

// listenSpoof opens the raw IPv4 socket for a profile and wraps it so reads and
// writes carry the full IP header. It needs privilege, and says so plainly.
func listenSpoof(profile SpoofProfile, iface string) (net.PacketConn, *ipv4.RawConn, error) {
	network := fmt.Sprintf("ip4:%d", profile.ipProtocol())
	pc, err := net.ListenPacket(network, "0.0.0.0")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, nil, fmt.Errorf("the spoof transport needs a raw IP socket, which requires root or CAP_NET_RAW: %w", err)
		}
		return nil, nil, fmt.Errorf("spoof: could not open the raw IP socket: %w", err)
	}
	if iface != "" {
		if err := bindPacketConnToInterface(pc, iface); err != nil {
			pc.Close()
			return nil, nil, fmt.Errorf("spoof: could not bind the raw socket to %q: %w", iface, err)
		}
	}
	raw, err := ipv4.NewRawConn(pc)
	if err != nil {
		pc.Close()
		return nil, nil, fmt.Errorf("spoof: could not take control of the IP header: %w", err)
	}
	return pc, raw, nil
}

func newSpoofConn(server bool, token string, profile SpoofProfile, spoofSrcIP, iface string) (net.PacketConn, error) {
	pc, raw, err := listenSpoof(profile, iface)
	if err != nil {
		return nil, err
	}
	tag, port := spoofIdentity(token)
	var src net.IP
	if spoofSrcIP != "" {
		if src = net.ParseIP(spoofSrcIP).To4(); src == nil {
			pc.Close()
			return nil, fmt.Errorf("spoof: %q is not a valid IPv4 address for spoof_src_ip", spoofSrcIP)
		}
	}
	return &spoofConn{
		raw: raw, pc: pc, proto: profile.ipProtocol(),
		profile: profile, server: server, tag: tag, port: port, spoofSrc: src,
	}, nil
}

func newSpoofServerConn(token string, profile SpoofProfile, spoofSrcIP, iface string) (net.PacketConn, error) {
	return newSpoofConn(true, token, profile, spoofSrcIP, iface)
}

func newSpoofClientConn(token string, profile SpoofProfile, spoofSrcIP, iface string) (net.PacketConn, error) {
	return newSpoofConn(false, token, profile, spoofSrcIP, iface)
}

// WriteTo wraps a KCP datagram in the profile's L4 shim and a hand-built IP
// header, forging the source address, and sends it to the real destination.
func (c *spoofConn) WriteTo(p []byte, dst net.Addr) (int, error) {
	dstIP, err := toIPAddr(dst)
	if err != nil {
		return 0, err
	}
	realDst := dstIP.IP.To4()
	if realDst == nil {
		return 0, fmt.Errorf("spoof: destination %v is not IPv4", dstIP.IP)
	}

	// The source written into the header (and the L4 checksum): the forged one
	// if configured, otherwise the real local address toward this destination,
	// resolved once and cached so the checksum matches what goes on the wire.
	src := c.spoofSrc
	if src == nil {
		src = c.localSourceToward(realDst)
	}

	framed := encodeXdiPayload(c.tag, outboundDir(c.server), p)
	shim := c.buildShim(src, realDst, framed)

	h := &ipv4.Header{
		Version:  ipv4.Version,
		Len:      ipv4.HeaderLen,
		TOS:      0,
		TotalLen: ipv4.HeaderLen + len(shim),
		ID:       int(c.ipID.Add(1) & 0xffff),
		Flags:    ipv4.DontFragment,
		TTL:      64,
		Protocol: c.proto,
		Src:      src,
		Dst:      realDst,
		// Checksum left 0: with a header-included raw socket the kernel fills
		// the IP checksum in for us.
	}
	if err := c.raw.WriteTo(h, shim, nil); err != nil {
		return 0, err
	}
	// Report the caller's byte count, not the wire count: KCP measures what it
	// asked to send; our framing is not its concern.
	return len(p), nil
}

// ReadFrom returns the next datagram addressed to this tunnel, dropping raw IP
// traffic that belongs to another tunnel, to a real service, or to a stray
// packet. It respects the read deadline across those drops. The address it
// returns is the real source seen in the packet's IP header.
func (c *spoofConn) ReadFrom(p []byte) (int, net.Addr, error) {
	buf := make([]byte, len(p)+spoofOverhead(c.profile)+64)
	wantDir := inboundDir(c.server)
	for {
		h, payload, _, err := c.raw.ReadFrom(buf)
		if err != nil {
			return 0, nil, err
		}
		l4, ok := c.stripShim(payload)
		if !ok {
			continue
		}
		inner, ok := decodeXdiPayload(c.tag, wantDir, l4)
		if !ok {
			continue
		}
		return copy(p, inner), &net.IPAddr{IP: h.Src}, nil
	}
}

func (c *spoofConn) Close() error                       { return c.raw.Close() }
func (c *spoofConn) LocalAddr() net.Addr                { return c.pc.LocalAddr() }
func (c *spoofConn) SetDeadline(t time.Time) error      { return c.raw.SetDeadline(t) }
func (c *spoofConn) SetReadDeadline(t time.Time) error  { return c.raw.SetReadDeadline(t) }
func (c *spoofConn) SetWriteDeadline(t time.Time) error { return c.raw.SetWriteDeadline(t) }

// localSourceToward returns the real local address the kernel would use to
// reach dst, cached per destination. It matters only when no source is being
// forged: the header source must then match the checksum's pseudo-header, so
// both use this value rather than leaving the kernel to pick after the fact.
func (c *spoofConn) localSourceToward(dst net.IP) net.IP {
	key := dst.String()
	if v, ok := c.srcCache.Load(key); ok {
		return v.(net.IP)
	}
	src := net.IPv4zero.To4()
	if u, err := net.Dial("udp", net.JoinHostPort(key, "9")); err == nil {
		if la, ok := u.LocalAddr().(*net.UDPAddr); ok && la.IP.To4() != nil {
			src = la.IP.To4()
		}
		u.Close()
	}
	c.srcCache.Store(key, src)
	return src
}

// buildShim wraps the framed payload in the profile's L4 header, with the
// checksum computed over the given source and destination. The tcp profile's
// sequence number advances per packet, which is this conn's only mutable input
// to the otherwise pure packet-crafting in spoofpacket.go.
func (c *spoofConn) buildShim(src, dst net.IP, framed []byte) []byte {
	seq := c.tcpSeq.Add(uint32(len(framed)) + 1)
	return buildSpoofShim(c.profile, c.port, seq, src, dst, framed)
}

// stripShim validates the L4 header of an incoming packet and returns the framed
// payload inside, or ok=false if it is not addressed to this tunnel's port.
func (c *spoofConn) stripShim(l4 []byte) ([]byte, bool) {
	return stripSpoofShim(c.profile, c.port, l4)
}
