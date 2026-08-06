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
//   - RECV is a separate socket. For the udp profile it is an ORDINARY UDP
//     socket, because the far end addresses its packets to this host's real IP
//     and the kernel delivers them normally. For tcp/icmp it is a raw socket,
//     since no ordinary socket would accept that framing.
//   - The far end forges ITS source too, so a reply cannot be routed by the
//     source we observe. Routing therefore always uses the peer's real address,
//     known ahead of time: the client resolves it from RemoteAddr, and the
//     server is told it (spoof_peer_ip). ReadFrom always reports that fixed real
//     peer, so KCP's replies go to the right place.
//
// The send and receive profiles are chosen independently — the "asymmetric"
// case, for a path whose filtering differs by direction. On a symmetric tunnel
// they are equal. The side (client/server) decides which of the uplink and
// downlink profiles is the send one and which the receive one.
//
// Whether a given forged source actually traverses the network is a property of
// the route, not this code. The spoof-capability tester exists to find the ones
// that do. Experimental, Linux only, and costs a raw socket (root/CAP_NET_RAW).

// spoofConn is one tunnel's carrier. Exactly one of udpRecv / rawRecv is set,
// depending on the receive profile.
type spoofConn struct {
	send        *ipv4.RawConn  // raw send, with the IP header under our control
	sendPC      net.PacketConn // kept for Close
	sendProfile SpoofProfile

	udpRecv     *net.UDPConn   // set when recvProfile is udp
	rawRecv     *ipv4.RawConn  // set when recvProfile is tcp/icmp
	rawRecvPC   net.PacketConn // kept for Close/LocalAddr
	recvProfile SpoofProfile

	server   bool
	tag      [xdiTagLen]byte
	port     uint16
	realPeer net.IP
	spoofSrc net.IP

	rst     *rstGuard // the tcp RST-suppression rule, set when recvProfile is tcp
	ipID    atomic.Uint32
	tcpSeq  atomic.Uint32
	icmpSeq atomic.Uint32
}

// spoofOverhead is what a profile's framing costs on top of the payload, so KCP
// can be told a small enough MTU that a datagram never fragments.
func spoofOverhead(p SpoofProfile) int {
	return ipv4.HeaderLen + profileL4Len(p) + xdiHeaderLen
}

// openRawIP opens a raw IPv4 socket for a protocol, optionally pinned to a
// device, and wraps it so reads and writes carry the full IP header.
func openRawIP(proto int, iface string) (net.PacketConn, *ipv4.RawConn, error) {
	pc, err := net.ListenPacket("ip4:"+strconv.Itoa(proto), "0.0.0.0")
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

// newSpoofConn opens the send and receive sockets for one tunnel. uplink is the
// client→server profile and downlink the server→client one; this side's send
// profile is the direction it originates and its receive profile the other.
func newSpoofConn(server bool, token string, uplink, downlink SpoofProfile, realPeer net.IP, srcIP string, srcPool []string, iface string) (net.PacketConn, error) {
	realPeer = realPeer.To4()
	if realPeer == nil {
		return nil, fmt.Errorf("spoof: the peer's real IPv4 address is required")
	}
	spoofSrc, err := chooseSpoofSrc(srcIP, srcPool)
	if err != nil {
		return nil, err
	}
	tag, port := spoofIdentity(token)

	// The client sends on the uplink and receives on the downlink; the server is
	// the mirror.
	sendProfile, recvProfile := uplink, downlink
	if server {
		sendProfile, recvProfile = downlink, uplink
	}

	sendPC, send, err := openRawIP(sendProfile.ipProtocol(), iface)
	if err != nil {
		return nil, err
	}

	c := &spoofConn{
		send: send, sendPC: sendPC, sendProfile: sendProfile,
		recvProfile: recvProfile, server: server, tag: tag, port: port,
		realPeer: realPeer, spoofSrc: spoofSrc,
	}

	if recvProfile == SpoofProfileUDP {
		// Ordinary UDP receive socket: the peer's forged packets are addressed to
		// this host's real IP and this port, so the kernel delivers them here.
		recv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: int(port)})
		if err != nil {
			send.Close()
			return nil, fmt.Errorf("spoof: could not open the udp receive socket on port %d: %w", port, err)
		}
		c.udpRecv = recv
		return c, nil
	}

	// tcp/icmp receive: a raw socket. For tcp the host kernel would answer the
	// forged segments with a RST; a targeted rule drops just those. icmp needs no
	// rule — the frame's direction byte discards the kernel's automatic echo
	// reply.
	rawRecvPC, rawRecv, err := openRawIP(recvProfile.ipProtocol(), iface)
	if err != nil {
		send.Close()
		return nil, err
	}
	c.rawRecv, c.rawRecvPC = rawRecv, rawRecvPC
	if recvProfile == SpoofProfileTCP {
		c.rst = installRSTGuard(port)
	}
	return c, nil
}

func newSpoofServerConn(token string, uplink, downlink SpoofProfile, realPeer net.IP, srcIP string, srcPool []string, iface string) (net.PacketConn, error) {
	return newSpoofConn(true, token, uplink, downlink, realPeer, srcIP, srcPool, iface)
}

func newSpoofClientConn(token string, uplink, downlink SpoofProfile, realPeer net.IP, srcIP string, srcPool []string, iface string) (net.PacketConn, error) {
	return newSpoofConn(false, token, uplink, downlink, realPeer, srcIP, srcPool, iface)
}

// sourceFor returns the source address to stamp and to checksum with: the forged
// one if configured, otherwise the real local address toward the peer (a safety
// net — a spoof tunnel normally sets a source).
func (c *spoofConn) sourceFor() net.IP {
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

// WriteTo wraps a KCP datagram in the send profile's shim and a hand-built IP
// header, forging the source, and sends it to the real peer. The dst KCP passes
// is ignored for routing — it is always the real peer.
func (c *spoofConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	src := c.sourceFor()
	framed := encodeXdiPayload(c.tag, outboundDir(c.server), p)

	var shim []byte
	switch c.sendProfile {
	case SpoofProfileICMP:
		typ := byte(icmpTypeEchoRequest) // the client pings
		if c.server {
			typ = icmpTypeEchoReply // the server answers
		}
		shim = buildICMPEcho(typ, c.port, uint16(c.icmpSeq.Add(1)), framed)
	case SpoofProfileTCP:
		shim = buildTCPShim(c.port, c.tcpSeq.Add(uint32(len(framed))+1), src, c.realPeer, framed)
	default: // udp
		shim = buildUDPShim(c.port, src, c.realPeer, framed)
	}

	h := &ipv4.Header{
		Version:  ipv4.Version,
		Len:      ipv4.HeaderLen,
		TotalLen: ipv4.HeaderLen + len(shim),
		ID:       int(c.ipID.Add(1) & 0xffff),
		Flags:    ipv4.DontFragment,
		TTL:      64,
		Protocol: c.sendProfile.ipProtocol(),
		Src:      src,
		Dst:      c.realPeer,
	}
	if err := c.send.WriteTo(h, shim, nil); err != nil {
		return 0, err
	}
	return len(p), nil
}

// ReadFrom returns the next datagram addressed to this tunnel, dropping anything
// without this tunnel's tag and direction. The address it returns is always the
// real peer, so KCP routes its replies there rather than to the forged source.
func (c *spoofConn) ReadFrom(p []byte) (int, net.Addr, error) {
	peer := &net.IPAddr{IP: c.realPeer}
	wantDir := inboundDir(c.server)

	if c.udpRecv != nil {
		buf := make([]byte, len(p)+spoofOverhead(c.recvProfile)+64)
		for {
			n, _, err := c.udpRecv.ReadFromUDP(buf)
			if err != nil {
				return 0, nil, err
			}
			if inner, ok := decodeXdiPayload(c.tag, wantDir, buf[:n]); ok {
				return copy(p, inner), peer, nil
			}
		}
	}

	buf := make([]byte, len(p)+spoofOverhead(c.recvProfile)+64)
	for {
		_, payload, _, err := c.rawRecv.ReadFrom(buf)
		if err != nil {
			return 0, nil, err
		}
		var l4 []byte
		var ok bool
		if c.recvProfile == SpoofProfileICMP {
			l4, ok = parseICMPEcho(c.port, payload)
		} else {
			l4, ok = stripSpoofShim(c.recvProfile, c.port, payload)
		}
		if !ok {
			continue
		}
		if inner, ok := decodeXdiPayload(c.tag, wantDir, l4); ok {
			return copy(p, inner), peer, nil
		}
	}
}

func (c *spoofConn) Close() error {
	if c.rst != nil {
		c.rst.remove()
	}
	c.send.Close()
	if c.udpRecv != nil {
		return c.udpRecv.Close()
	}
	return c.rawRecv.Close()
}

func (c *spoofConn) LocalAddr() net.Addr {
	if c.udpRecv != nil {
		return c.udpRecv.LocalAddr()
	}
	return c.rawRecvPC.LocalAddr()
}

func (c *spoofConn) SetDeadline(t time.Time) error {
	if c.udpRecv != nil {
		return c.udpRecv.SetDeadline(t)
	}
	return c.rawRecv.SetDeadline(t)
}

func (c *spoofConn) SetReadDeadline(t time.Time) error {
	if c.udpRecv != nil {
		return c.udpRecv.SetReadDeadline(t)
	}
	return c.rawRecv.SetReadDeadline(t)
}

func (c *spoofConn) SetWriteDeadline(t time.Time) error {
	if c.udpRecv != nil {
		return c.udpRecv.SetWriteDeadline(t)
	}
	return c.rawRecv.SetWriteDeadline(t)
}
