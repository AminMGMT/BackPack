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

	server    bool
	port      uint16
	realPeer  net.IP
	spoofSrcs []net.IP // forged sources, rotated one per packet; empty = no spoofing

	rst     *rstGuard // the tcp RST-suppression rule, set when recvProfile is tcp
	rot     atomic.Uint32
	ipID    atomic.Uint32
	tcpSeq  atomic.Uint32
	icmpSeq atomic.Uint32
}

// spoofOverhead is what a profile's headers cost on top of the payload, so KCP
// can be told a small enough MTU that a datagram never fragments.
//
// It no longer counts a tag/direction prefix. That framing was borrowed from
// xdi and has been dropped so the carrier's ICMP and UDP match the reference
// spoof transports, which carry the payload bare and let the encryption above
// authenticate it. Only the IP header and the profile's L4 header remain.
func spoofOverhead(p SpoofProfile) int {
	return ipv4.HeaderLen + profileL4Len(p)
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
	spoofSrcs, err := parseSpoofPool(srcIP, srcPool)
	if err != nil {
		return nil, err
	}
	// No forged source configured: resolve the real local address toward the
	// peer once, so every packet's header and checksum agree and the send path
	// never has to look it up again. This is only the no-spoof safety net; a
	// real spoof tunnel always sets a pool.
	if len(spoofSrcs) == 0 {
		spoofSrcs = []net.IP{localSourceToward(realPeer)}
	}
	// Only the port is used now — as the ICMP identifier and the UDP/TCP port
	// the receiver filters on. The tag half of the identity backed the
	// tag/direction frame, which this carrier no longer writes.
	_, port := spoofIdentity(token)

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
		recvProfile: recvProfile, server: server, port: port,
		realPeer: realPeer, spoofSrcs: spoofSrcs,
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
	// rule — the receiver keeps only Echo Requests, so the kernel's automatic
	// Echo Reply is ignored.
	rawRecvPC, rawRecv, err := openRawIP(recvProfile.ipProtocol(), iface)
	if err != nil {
		send.Close()
		return nil, err
	}
	c.rawRecv, c.rawRecvPC = rawRecv, rawRecvPC
	// Push the demux into the kernel so the read loop only wakes for this
	// tunnel's flow. Best effort: ReadFrom re-checks the port/identifier if the
	// kernel refuses the filter, so correctness holds regardless and the error
	// is ignored.
	_ = attachSpoofBPF(rawRecvPC, recvProfile, port)
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

// sourceFor returns the source address to stamp on the next packet: the pool is
// rotated one address per packet, so the tunnel's volume is spread across every
// forged source and a source that starts being dropped costs only its share
// rather than the whole tunnel. The pool always holds at least one address (the
// real local one when nothing is forged), so this never allocates or blocks.
func (c *spoofConn) sourceFor() net.IP {
	n := len(c.spoofSrcs)
	return c.spoofSrcs[int(c.rot.Add(1)-1)%n]
}

// localSourceToward returns the real local address the kernel would use to reach
// dst, resolved once at construction. Used only when nothing is forged, so the
// packet's header source matches its checksum's pseudo-header.
func localSourceToward(dst net.IP) net.IP {
	if u, err := net.Dial("udp", net.JoinHostPort(dst.String(), "9")); err == nil {
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

	// The KCP datagram goes on the wire as-is, inside the profile's L4 header —
	// no tag or direction byte in front of it. That prefix was xdi's, carried
	// over when this transport was first built; the reference spoof transports
	// send the payload bare and rely on the encryption above to reject anything
	// that is not the tunnel's. Dropping it makes an ICMP packet a plain ping
	// and a UDP packet a plain datagram, which is what a capture should show.
	var shim []byte
	switch c.sendProfile {
	case SpoofProfileICMP:
		// Both ends send an Echo Request (type 8), as the reference does: the
		// receiver keeps only type 8, so the kernel's automatic Echo Reply to an
		// inbound request is discarded by the type alone, with no direction byte
		// to carry it.
		shim = buildICMPEcho(icmpTypeEchoRequest, c.port, uint16(c.icmpSeq.Add(1)), p)
	case SpoofProfileTCP:
		shim = buildTCPShim(c.port, c.tcpSeq.Add(uint32(len(p))+1), src, c.realPeer, p)
	default: // udp
		shim = buildUDPShim(c.port, src, c.realPeer, p)
	}

	h := &ipv4.Header{
		Version:  ipv4.Version,
		Len:      ipv4.HeaderLen,
		TotalLen: ipv4.HeaderLen + len(shim),
		ID:       int(c.ipID.Add(1) & 0xffff),
		// Don't-Fragment is deliberately NOT set: on a path whose MTU is smaller
		// than expected, DF turns an oversize packet into a silent black hole —
		// the "fragmentation needed" ICMP goes to the forged source and is lost —
		// whereas without it the packet fragments and still arrives. KCP already
		// sizes datagrams under the MTU, so fragmentation is rare regardless.
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

	// The udp profile receives on an ordinary UDP socket, so the kernel has
	// already stripped the IP and UDP headers and what arrives is the payload.
	// It is handed up untouched; the encryption above decides whether it is the
	// tunnel's, exactly as the reference does.
	if c.udpRecv != nil {
		buf := make([]byte, len(p)+spoofOverhead(c.recvProfile)+64)
		for {
			n, _, err := c.udpRecv.ReadFromUDP(buf)
			if err != nil {
				return 0, nil, err
			}
			if n == 0 {
				continue
			}
			return copy(p, buf[:n]), peer, nil
		}
	}

	// The tcp/icmp profiles receive on a raw socket, so the L4 header is still
	// present and its payload has to be lifted out. The demux is the L4 port
	// (tcp) or the echo identifier (icmp), the same field the kernel BPF filter
	// already matched on; there is no frame to check beyond that.
	buf := make([]byte, len(p)+spoofOverhead(c.recvProfile)+64)
	for {
		_, payload, _, err := c.rawRecv.ReadFrom(buf)
		if err != nil {
			return 0, nil, err
		}
		var inner []byte
		var ok bool
		if c.recvProfile == SpoofProfileICMP {
			inner, ok = parseICMPEcho(c.port, payload)
		} else {
			inner, ok = stripSpoofShim(c.recvProfile, c.port, payload)
		}
		if !ok || len(inner) == 0 {
			continue
		}
		return copy(p, inner), peer, nil
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
