//go:build linux

package network

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"golang.org/x/net/ipv4"
)

// A net.PacketConn that carries the tunnel's KCP datagrams inside raw IPv4
// packets whose source address is forged — the "IP Spoofing" transport.
//
// It is the same shape as the xdi ICMP carrier: KCP, its forward error
// correction and its encryption all sit on top unchanged, because to them this
// is just a net.PacketConn like the UDP socket they usually get.
//
// The mechanism, which every working IP-spoofing tunnel shares:
//
//   - SEND is a raw socket. The packet is routed to the peer's REAL address, so
//     it genuinely arrives, but the source in the IP header is replaced with a
//     forged one. That forged source is what a stateless L3 filter sees.
//   - RECV is an ORDINARY UDP socket. Because the far end addresses its packets
//     to this host's real IP, the kernel delivers them to a normal socket like
//     any other datagram — no raw capture of the whole host, and no ICMP
//     "port unreachable" leaking back to the forged source.
//   - The far end forges ITS source too, so a reply cannot be routed by the
//     source we observe. Routing therefore always uses the peer's real address,
//     which is known ahead of time: the client resolves it from RemoteAddr, and
//     the server is told it (spoof_peer_ip) because it cannot learn it from the
//     forged packets. ReadFrom always reports that fixed real peer, so KCP's
//     replies go to the right place.
//
// Whether a given forged source actually traverses the network is a property of
// the route, not this code. The spoof-capability tester exists to find the ones
// that do.
//
// This carrier implements the udp profile. Experimental, Linux only, and costs
// a raw socket for the send side (root or CAP_NET_RAW).

// spoofConn is one tunnel's carrier: a raw send socket paired with an ordinary
// UDP receive socket.
type spoofConn struct {
	send    *ipv4.RawConn  // raw send, with the IP header under our control
	sendPC  net.PacketConn // kept for Close
	recv    *net.UDPConn   // ordinary UDP socket the peer's packets arrive on
	profile SpoofProfile

	server   bool
	tag      [xdiTagLen]byte
	port     uint16 // the UDP port both ends use (token-derived)
	realPeer net.IP // where packets are actually routed
	spoofSrc net.IP // forged source stamped on every packet (may be nil)

	ipID   atomic.Uint32
	tcpSeq atomic.Uint32 // unused by udp; kept for the future tcp profile
}

// spoofOverhead is what the carrier's framing costs on top of the payload, so
// KCP can be told a small enough MTU that a datagram never fragments.
func spoofOverhead(p SpoofProfile) int {
	return ipv4.HeaderLen + profileL4Len(p) + xdiHeaderLen
}

// newSpoofConn opens the sockets for one tunnel, dispatching on the profile: udp
// pairs a raw sender with an ordinary UDP receiver (the kernel delivers the
// forged packets normally); tcp and icmp send and receive on one raw socket,
// because there is no ordinary socket that would accept their framing.
func newSpoofConn(server bool, token string, profile SpoofProfile, realPeer net.IP, srcIP string, srcPool []string, iface string) (net.PacketConn, error) {
	realPeer = realPeer.To4()
	if realPeer == nil {
		return nil, fmt.Errorf("spoof: the peer's real IPv4 address is required")
	}
	spoofSrc, err := chooseSpoofSrc(srcIP, srcPool)
	if err != nil {
		return nil, err
	}
	tag, port := spoofIdentity(token)

	// Raw send socket. IPPROTO_RAW hands us the IP header; we never read from it.
	sendPC, err := net.ListenPacket("ip4:"+strconv.Itoa(profile.ipProtocol()), "0.0.0.0")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("the spoof transport needs a raw IP socket, which requires root or CAP_NET_RAW: %w", err)
		}
		return nil, fmt.Errorf("spoof: could not open the raw send socket: %w", err)
	}
	if iface != "" {
		if err := bindPacketConnToInterface(sendPC, iface); err != nil {
			sendPC.Close()
			return nil, fmt.Errorf("spoof: could not bind the raw socket to %q: %w", iface, err)
		}
	}
	send, err := ipv4.NewRawConn(sendPC)
	if err != nil {
		sendPC.Close()
		return nil, fmt.Errorf("spoof: could not take control of the IP header: %w", err)
	}

	if profile == SpoofProfileUDP {
		// Ordinary UDP receive socket, bound to the token-derived port on all
		// addresses. The peer's forged packets are addressed to this host's real
		// IP and this port, so the kernel delivers them here normally.
		recv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: int(port)})
		if err != nil {
			send.Close()
			return nil, fmt.Errorf("spoof: could not open the udp receive socket on port %d: %w", port, err)
		}
		return &spoofConn{
			send: send, sendPC: sendPC, recv: recv, profile: profile,
			server: server, tag: tag, port: port, realPeer: realPeer, spoofSrc: spoofSrc,
		}, nil
	}

	// tcp/icmp: read the replies off the same raw socket we send on. For tcp the
	// host kernel would answer the forged segments with a RST; a targeted rule
	// drops just those so the flow survives (best effort — a warning if it can
	// not be installed). icmp needs no rule: the frame's direction byte discards
	// the kernel's automatic echo reply.
	rc := &spoofRawConn{
		raw: send, rawPC: sendPC, profile: profile,
		server: server, tag: tag, port: port, realPeer: realPeer, spoofSrc: spoofSrc,
	}
	if profile == SpoofProfileTCP {
		rc.rst = installRSTGuard(port)
	}
	return rc, nil
}

func newSpoofServerConn(token string, profile SpoofProfile, realPeer net.IP, srcIP string, srcPool []string, iface string) (net.PacketConn, error) {
	return newSpoofConn(true, token, profile, realPeer, srcIP, srcPool, iface)
}

func newSpoofClientConn(token string, profile SpoofProfile, realPeer net.IP, srcIP string, srcPool []string, iface string) (net.PacketConn, error) {
	return newSpoofConn(false, token, profile, realPeer, srcIP, srcPool, iface)
}

// WriteTo wraps a KCP datagram in a UDP shim and a hand-built IP header, forging
// the source, and sends it to the real peer. The dst KCP passes is ignored for
// routing — it is always the real peer — but its presence keeps the PacketConn
// contract intact.
func (c *spoofConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	src := c.spoofSrc
	if src == nil {
		// No spoofing configured: let the kernel fill the source by leaving it
		// zero, and use the real peer for the checksum pseudo-header via a
		// best-effort local address. In practice a spoof tunnel always sets a
		// source, so this path is just a safety net.
		src = net.IPv4zero.To4()
	}

	framed := encodeXdiPayload(c.tag, outboundDir(c.server), p)
	shim := buildSpoofShim(c.profile, c.port, c.tcpSeq.Add(uint32(len(framed))+1), src, c.realPeer, framed)

	h := &ipv4.Header{
		Version:  ipv4.Version,
		Len:      ipv4.HeaderLen,
		TotalLen: ipv4.HeaderLen + len(shim),
		ID:       int(c.ipID.Add(1) & 0xffff),
		Flags:    ipv4.DontFragment,
		TTL:      64,
		Protocol: 17,
		Src:      src,
		Dst:      c.realPeer,
	}
	if err := c.send.WriteTo(h, shim, nil); err != nil {
		return 0, err
	}
	return len(p), nil
}

// ReadFrom returns the next datagram addressed to this tunnel, dropping anything
// that arrives on the port without this tunnel's tag and direction. The address
// it returns is always the real peer, so KCP routes its replies there rather
// than to the forged source the packet carried.
func (c *spoofConn) ReadFrom(p []byte) (int, net.Addr, error) {
	buf := make([]byte, len(p)+spoofOverhead(c.profile)+64)
	wantDir := inboundDir(c.server)
	peer := &net.IPAddr{IP: c.realPeer}
	for {
		n, _, err := c.recv.ReadFromUDP(buf)
		if err != nil {
			return 0, nil, err
		}
		inner, ok := decodeXdiPayload(c.tag, wantDir, buf[:n])
		if !ok {
			continue
		}
		return copy(p, inner), peer, nil
	}
}

func (c *spoofConn) Close() error {
	c.send.Close()
	return c.recv.Close()
}
func (c *spoofConn) LocalAddr() net.Addr                { return c.recv.LocalAddr() }
func (c *spoofConn) SetDeadline(t time.Time) error      { return c.recv.SetDeadline(t) }
func (c *spoofConn) SetReadDeadline(t time.Time) error  { return c.recv.SetReadDeadline(t) }
func (c *spoofConn) SetWriteDeadline(t time.Time) error { return c.recv.SetWriteDeadline(t) }

// spoofRawConn is the tcp/icmp carrier: one raw socket both sends the forged
// packets and reads the replies, since neither profile's framing would be
// accepted by an ordinary socket. Routing and demux are identical to the udp
// carrier — always the real peer, always by tag and direction.
type spoofRawConn struct {
	raw     *ipv4.RawConn
	rawPC   net.PacketConn
	profile SpoofProfile

	server   bool
	tag      [xdiTagLen]byte
	port     uint16
	realPeer net.IP
	spoofSrc net.IP

	rst     *rstGuard // the tcp RST-suppression rule, nil for icmp
	ipID    atomic.Uint32
	tcpSeq  atomic.Uint32
	icmpSeq atomic.Uint32
}

// localSourceToward returns a source address the tcp checksum can use when no
// source is being forged: the checksum must match what actually goes on the
// wire, so we resolve the address the kernel would pick for the real peer. A
// spoof tunnel normally sets a source, so this is only a safety net.
func (c *spoofRawConn) sourceFor() net.IP {
	if c.spoofSrc != nil {
		return c.spoofSrc
	}
	if u, err := net.Dial("udp", net.JoinHostPort(c.realPeer.String(), "9")); err == nil {
		defer u.Close()
		if la, ok := u.LocalAddr().(*net.UDPAddr); ok && la.IP.To4() != nil {
			return la.IP.To4()
		}
	}
	return net.IPv4zero.To4()
}

func (c *spoofRawConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	src := c.sourceFor()
	framed := encodeXdiPayload(c.tag, outboundDir(c.server), p)

	var shim []byte
	switch c.profile {
	case SpoofProfileICMP:
		typ := byte(icmpTypeEchoRequest) // the client pings
		if c.server {
			typ = icmpTypeEchoReply // the server answers
		}
		shim = buildICMPEcho(typ, c.port, uint16(c.icmpSeq.Add(1)), framed)
	default: // tcp
		shim = buildTCPShim(c.port, c.tcpSeq.Add(uint32(len(framed))+1), src, c.realPeer, framed)
	}

	h := &ipv4.Header{
		Version:  ipv4.Version,
		Len:      ipv4.HeaderLen,
		TotalLen: ipv4.HeaderLen + len(shim),
		ID:       int(c.ipID.Add(1) & 0xffff),
		Flags:    ipv4.DontFragment,
		TTL:      64,
		Protocol: c.profile.ipProtocol(),
		Src:      src,
		Dst:      c.realPeer,
	}
	if err := c.raw.WriteTo(h, shim, nil); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *spoofRawConn) ReadFrom(p []byte) (int, net.Addr, error) {
	buf := make([]byte, len(p)+spoofOverhead(c.profile)+64)
	wantDir := inboundDir(c.server)
	peer := &net.IPAddr{IP: c.realPeer}
	for {
		_, payload, _, err := c.raw.ReadFrom(buf)
		if err != nil {
			return 0, nil, err
		}
		var l4 []byte
		var ok bool
		if c.profile == SpoofProfileICMP {
			l4, ok = parseICMPEcho(c.port, payload)
		} else {
			l4, ok = stripSpoofShim(c.profile, c.port, payload)
		}
		if !ok {
			continue
		}
		inner, ok := decodeXdiPayload(c.tag, wantDir, l4)
		if !ok {
			continue
		}
		return copy(p, inner), peer, nil
	}
}

func (c *spoofRawConn) Close() error {
	if c.rst != nil {
		c.rst.remove()
	}
	return c.raw.Close()
}
func (c *spoofRawConn) LocalAddr() net.Addr                { return c.rawPC.LocalAddr() }
func (c *spoofRawConn) SetDeadline(t time.Time) error      { return c.raw.SetDeadline(t) }
func (c *spoofRawConn) SetReadDeadline(t time.Time) error  { return c.raw.SetReadDeadline(t) }
func (c *spoofRawConn) SetWriteDeadline(t time.Time) error { return c.raw.SetWriteDeadline(t) }
