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

	udpRecv     *net.UDPConn   // set when recvProfile is udp and XDP is off
	rawRecv     *ipv4.RawConn  // set when recvProfile is tcp/icmp and XDP is off
	rawRecvPC   net.PacketConn // kept for Close/LocalAddr
	recvProfile SpoofProfile

	// xdp, when set, is the kernel XDP receive fast path; it replaces udpRecv /
	// rawRecv entirely, so both of those stay nil and the RST guard and ICMP
	// suppression are unnecessary (the kernel never processes the dropped
	// packets). xdpNote records why the fast path is or is not in use, for the log.
	xdp     *spoofXDPReceiver
	xdpNote string

	server    bool
	port      uint16
	realPeer  net.IP
	spoofSrcs []net.IP // forged sources, rotated one per packet; empty = no spoofing
	readBuf   []byte   // reused by ReadFrom so the receive loop never allocates
	mtu       int      // sends larger than this are IP-fragmented

	sendICMPType byte     // echo type stamped on outgoing icmp/icmpv6 packets
	recvICMPType byte     // echo type accepted on incoming icmp/icmpv6 packets
	expectSrc    net.IP   // required forged source of inbound packets, nil = accept any
	dpi          SpoofDPI // optional obfuscation applied on send and undone on receive

	rst      *rstGuard // the tcp RST-suppression rule, set when recvProfile is tcp
	icmpEcho bool      // holds the kernel Echo-Reply suppression, set when recvProfile is icmp
	rot      atomic.Uint32
	tcpSeq   atomic.Uint32
	icmpSeq  atomic.Uint32
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

// protoRawSend is IPPROTO_RAW: a send-only raw socket whose IP header is
// entirely the caller's, and which is handed no inbound packets at all.
const protoRawSend = 255

// openRawIP opens a raw IPv4 socket for a protocol, optionally pinned to a
// device, and wraps it so reads and writes carry the full IP header. sockBuf
// sizes its send and receive buffers.
func openRawIP(proto int, iface string, sockBuf int) (net.PacketConn, *ipv4.RawConn, error) {
	pc, err := net.ListenPacket("ip4:"+strconv.Itoa(proto), "0.0.0.0")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, nil, fmt.Errorf("the spoof transport needs a raw IP socket, which requires root or CAP_NET_RAW: %w", err)
		}
		return nil, nil, fmt.Errorf("spoof: could not open the raw IP socket: %w", err)
	}
	applySockBuf(pc, sockBuf)
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

// newSpoofConn opens the send and receive sockets for one tunnel. This side's
// send profile is the direction it originates (uplink for the client, downlink
// for the server) and its receive profile the other.
func newSpoofConn(server bool, o spoofConnOpts) (net.PacketConn, error) {
	realPeer := o.realPeer.To4()
	if realPeer == nil {
		return nil, fmt.Errorf("spoof: the peer's real IPv4 address is required")
	}
	sockBuf := o.sockBuf
	if sockBuf <= 0 {
		sockBuf = defaultSpoofSockBuf
	}
	mtu := o.mtu
	if mtu <= ipv4.HeaderLen+profileL4Len(SpoofProfileTCP) {
		mtu = defaultSpoofMTU
	}
	spoofSrcs, err := parseSpoofPool(o.srcIP, o.srcPool)
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
	// The expected forged source of inbound packets, if the operator pinned it.
	// A packet whose source is not this drops before the encryption sees it.
	var expectSrc net.IP
	if o.peerSrc != "" {
		expectSrc = net.ParseIP(o.peerSrc).To4()
		if expectSrc == nil {
			return nil, fmt.Errorf("spoof: spoof_peer_src_ip %q is not a valid IPv4 address", o.peerSrc)
		}
	}
	// Only the port is used now — as the ICMP identifier and the UDP/TCP port
	// the receiver filters on. The tag half of the identity backed the
	// tag/direction frame, which this carrier no longer writes.
	_, port := spoofIdentity(o.token)

	// The client sends on the uplink and receives on the downlink; the server is
	// the mirror.
	sendProfile, recvProfile := o.uplink, o.downlink
	if server {
		sendProfile, recvProfile = o.downlink, o.uplink
	}

	// IPPROTO_RAW, not the profile's protocol.
	//
	// A raw socket opened on a protocol receives every packet of that protocol
	// the host sees, and this one is only ever written to — so opening it on,
	// say, IPPROTO_UDP left the kernel queueing a copy of every UDP packet on
	// the machine into a buffer nothing would ever read. IPPROTO_RAW is
	// send-only by definition and carries IP_HDRINCL implicitly, which is what
	// the reference opens and what this socket actually needs.
	sendPC, send, err := openRawIP(protoRawSend, o.iface, sockBuf)
	if err != nil {
		return nil, err
	}

	c := &spoofConn{
		send: send, sendPC: sendPC, sendProfile: sendProfile,
		recvProfile: recvProfile, server: server, port: port,
		realPeer: realPeer, spoofSrcs: spoofSrcs, mtu: mtu, expectSrc: expectSrc,
		dpi: o.dpi,
		// One reusable receive buffer, sized for the largest datagram the carrier
		// will ever lift out plus its L4 shim. ReadFrom copies the payload into the
		// caller's slice before returning, and it is only ever called from a single
		// reader (KCP's input loop, or the pipe's spoof→udp goroutine), so reusing
		// one buffer is safe and takes the per-packet allocation out of the hot
		// path — the allocation that showed up under load on the old carrier.
		readBuf: make([]byte, 65535),
	}
	// Resolve the echo types for the icmp/icmpv6 family. With the reply split off
	// (the default, matching spoof-tunnel) both ends send requests. With it on
	// (matching Candy's realistic ping pair) the client sends requests and the
	// server sends replies, so each side accepts the type its peer sends.
	sReq, sRep := sendProfile.icmpEchoTypes()
	c.sendICMPType = sReq
	if o.replySplit && server {
		c.sendICMPType = sRep
	}
	rReq, rRep := recvProfile.icmpEchoTypes()
	c.recvICMPType = rReq
	if o.replySplit && !server {
		c.recvICMPType = rRep // the client's peer is the server, which replies
	}

	// The XDP receive fast path, if a NIC was named. It captures this tunnel's
	// packets in the kernel and drops them before the socket layer, so on success
	// no ordinary receive socket is opened, and no RST guard or ICMP suppression
	// is needed — the kernel never processes what XDP dropped. On any failure the
	// note records why and we fall through to the ordinary receive below.
	if o.xdpIface != "" {
		portOff := -1
		switch {
		case recvProfile.isICMPFamily():
			portOff = 4 // ICMP/ICMPv6 echo identifier
		case recvProfile == SpoofProfileTCP, recvProfile == SpoofProfileUDP:
			portOff = 2 // UDP/TCP destination port
		}
		xr, err := newSpoofXDPReceiver(spoofXDPConfig{
			iface: o.xdpIface, proto: recvProfile.ipProtocol(),
			port: port, portOff: portOff, expectSrc: expectSrc, sockBuf: sockBuf,
		})
		if err != nil {
			c.xdpNote = "spoof: XDP receive unavailable, using the raw-socket receive: " + err.Error()
		} else {
			c.xdp = xr
			c.xdpNote = "spoof: XDP receive fast path attached to " + o.xdpIface
			return c, nil
		}
	}

	if recvProfile == SpoofProfileUDP {
		// Ordinary UDP receive socket: the peer's forged packets are addressed to
		// this host's real IP and this port, so the kernel delivers them here.
		recv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: int(port)})
		if err != nil {
			send.Close()
			return nil, fmt.Errorf("spoof: could not open the udp receive socket on port %d: %w", port, err)
		}
		applySockBuf(recv, sockBuf)
		c.udpRecv = recv
		return c, nil
	}

	// tcp/icmp/icmpv6 receive: a raw socket. For tcp the host kernel would answer
	// the forged segments with a RST; a targeted rule drops just those. icmp needs
	// no rule — the kernel's automatic Echo Reply is suppressed below.
	rawRecvPC, rawRecv, err := openRawIP(recvProfile.ipProtocol(), o.iface, sockBuf)
	if err != nil {
		send.Close()
		return nil, err
	}
	c.rawRecv, c.rawRecvPC = rawRecv, rawRecvPC
	// Push the demux into the kernel so the read loop only wakes for this
	// tunnel's flow. Best effort: ReadFrom re-checks the port/identifier if the
	// kernel refuses the filter, so correctness holds regardless and the error
	// is ignored. ipip/gre have no port to filter on, so the socket takes every
	// packet of the protocol and the source-IP pin plus the encryption sort it.
	if recvProfile.hasPortDemux() {
		_ = attachSpoofBPF(rawRecvPC, recvProfile, port, c.recvICMPType)
	}
	if recvProfile == SpoofProfileTCP {
		c.rst = installRSTGuard(port)
	}
	if recvProfile == SpoofProfileICMP {
		// Silence the kernel's automatic Echo Reply to the data-carrying Echo
		// Requests this carrier receives, as the reference spoof-tunnel does. Only
		// real IPv4 ICMP (proto 1) triggers a kernel reply; the icmpv6 profile
		// rides proto 58 in IPv4, which the kernel does not answer, so it needs no
		// suppression.
		acquireICMPEchoSuppression()
		c.icmpEcho = true
	}
	return c, nil
}

func newSpoofServerConn(o spoofConnOpts) (net.PacketConn, error) {
	return newSpoofConn(true, o)
}

func newSpoofClientConn(o spoofConnOpts) (net.PacketConn, error) {
	return newSpoofConn(false, o)
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

	// Obfuscation, applied to the payload before it is wrapped so the checksum
	// and the encryption above both cover it. Padding is innermost; the fake TLS
	// record header (tcp only) goes outside it so a middlebox sees the record at
	// the very start of the segment. Both must be set the same on the far end,
	// which strips them in the mirror order on receive.
	body := p
	if c.dpi.Padding {
		body = applyPadding(body, c.dpi.PaddingMax)
	}
	if c.dpi.FakeTLS && c.sendProfile == SpoofProfileTCP {
		body = applyFakeTLS(body)
	}

	// The datagram goes on the wire inside the profile's L4 header — no tag or
	// direction byte in front of it: the reference spoof transports send the
	// payload bare and rely on the encryption above to reject anything that is
	// not the tunnel's. The source port may be shuffled per packet; the
	// destination port stays fixed so the receiver's demux still matches.
	srcPort := c.dpi.pickSrcPort(c.port)
	var shim []byte
	switch {
	case c.sendProfile.isICMPFamily():
		// The echo type is c.sendICMPType: the request type when both ends send
		// requests (the default), or — with the reply split on — the request type
		// on the client and the reply type on the server, so the pair looks like a
		// real ping and its answer.
		shim = buildICMPEcho(c.sendICMPType, c.port, uint16(c.icmpSeq.Add(1)), body)
	case c.sendProfile == SpoofProfileTCP:
		shim = buildTCPShimPorts(srcPort, c.port, nextTCPSeq(&c.tcpSeq, len(body)), src, c.realPeer, body)
	case c.sendProfile == SpoofProfileIPIP, c.sendProfile == SpoofProfileProto58:
		shim = body // proto 4 / 58: the payload IS the IP body, no L4 header
	case c.sendProfile == SpoofProfileGRE:
		shim = buildGREShim(body)
	default: // udp
		shim = buildUDPShimPorts(srcPort, c.port, src, c.realPeer, body)
	}

	if err := c.sendIP(src, c.sendProfile.ipProtocol(), shim); err != nil {
		return 0, err
	}
	return len(p), nil
}

// sendIP writes one L4 segment to the real peer inside a hand-built IP header,
// forging the source. When the whole packet fits the MTU it goes as one; when it
// does not, it is split into IP fragments that the peer's kernel reassembles
// before the raw receive socket ever sees them. The L4 checksum already spans
// the full segment, so fragmenting it needs no recomputation.
//
// Don't-Fragment is deliberately never set. Fragmenting in userspace here is the
// belt to that suspenders: a raw IP_HDRINCL socket will not fragment for us and
// may reject an oversize packet outright, so on a path whose MTU is smaller than
// a datagram we do the split ourselves rather than relying on the kernel — while
// still leaving DF clear so an unexpectedly small link fragments rather than
// black-holing (the "fragmentation needed" ICMP would go to the forged source
// and be lost).
func (c *spoofConn) sendIP(src net.IP, proto int, shim []byte) error {
	id := int(randomIPID()) // all fragments of one segment share this id
	// TTL and DSCP are picked once per datagram (not per fragment) so every
	// fragment of one packet agrees, as a genuine packet's do. Both are ignored
	// by the receiver, so the jitter needs no coordination.
	ttl, tos := c.dpi.pickTTL(), c.dpi.pickDSCP()
	for _, f := range spoofFragments(len(shim), c.mtu) {
		flags := ipv4.HeaderFlags(0)
		if f.more {
			flags = ipv4.MoreFragments
		}
		h := &ipv4.Header{
			Version: ipv4.Version, Len: ipv4.HeaderLen, TOS: tos,
			TotalLen: ipv4.HeaderLen + (f.end - f.off), ID: id,
			Flags: flags, FragOff: f.off / 8,
			TTL: ttl, Protocol: proto, Src: src, Dst: c.realPeer,
		}
		if err := c.send.WriteTo(h, shim[f.off:f.end], nil); err != nil {
			return err
		}
	}
	return nil
}

// ReadFrom returns the next datagram addressed to this tunnel, dropping anything
// without this tunnel's tag and direction. The address it returns is always the
// real peer, so KCP routes its replies there rather than to the forged source.
func (c *spoofConn) ReadFrom(p []byte) (int, net.Addr, error) {
	peer := &net.IPAddr{IP: c.realPeer}

	// The XDP fast path hands up the L4 segment already lifted out of the frame,
	// with the forged source alongside it. The per-profile strip is identical to
	// the raw-socket path below — including the udp profile, whose 8-byte header
	// the XDP path leaves on (unlike the ordinary UDP socket, which the kernel has
	// already stripped) so stripSpoofShim removes it here.
	if c.xdp != nil {
		for {
			src, n, err := c.xdp.read(c.readBuf)
			if err != nil {
				return 0, nil, err
			}
			if n == 0 {
				continue
			}
			if c.expectSrc != nil && (src == nil || !src.Equal(c.expectSrc)) {
				continue
			}
			l4 := c.readBuf[:n]
			var inner []byte
			var ok bool
			switch {
			case c.recvProfile.isICMPFamily():
				inner, ok = parseICMPEcho(c.recvICMPType, c.port, l4)
			case c.recvProfile == SpoofProfileIPIP, c.recvProfile == SpoofProfileProto58:
				inner, ok = l4, true
			case c.recvProfile == SpoofProfileGRE:
				inner, ok = stripGREShim(l4)
			default: // udp and tcp both carry an L4 header the strip removes
				inner, ok = stripSpoofShim(c.recvProfile, c.port, l4)
			}
			if !ok {
				continue
			}
			inner, ok = c.undoDPI(c.recvProfile, inner)
			if !ok || len(inner) == 0 {
				continue
			}
			return copy(p, inner), peer, nil
		}
	}

	// The udp profile receives on an ordinary UDP socket, so the kernel has
	// already stripped the IP and UDP headers and what arrives is the payload.
	// It is handed up untouched; the encryption above decides whether it is the
	// tunnel's, exactly as the reference does.
	if c.udpRecv != nil {
		buf := c.readBuf
		for {
			n, from, err := c.udpRecv.ReadFromUDP(buf)
			if err != nil {
				return 0, nil, err
			}
			if n == 0 {
				continue
			}
			// Drop packets whose forged source is not the one the peer was pinned
			// to. Cheap, and it keeps foreign scans and stray datagrams from ever
			// reaching the encryption above.
			if c.expectSrc != nil && (from == nil || !from.IP.Equal(c.expectSrc)) {
				continue
			}
			inner, ok := c.undoDPI(SpoofProfileUDP, buf[:n])
			if !ok || len(inner) == 0 {
				continue
			}
			return copy(p, inner), peer, nil
		}
	}

	// The tcp/icmp/icmpv6 profiles receive on a raw socket, so the L4 header is
	// still present and its payload has to be lifted out. The demux is the L4 port
	// (tcp) or the echo identifier (icmp/icmpv6), the same field the kernel BPF
	// filter already matched on; there is no frame to check beyond that.
	buf := c.readBuf
	for {
		h, payload, _, err := c.rawRecv.ReadFrom(buf)
		if err != nil {
			return 0, nil, err
		}
		if c.expectSrc != nil && (h == nil || !h.Src.Equal(c.expectSrc)) {
			continue
		}
		var inner []byte
		var ok bool
		switch {
		case c.recvProfile.isICMPFamily():
			inner, ok = parseICMPEcho(c.recvICMPType, c.port, payload)
		case c.recvProfile == SpoofProfileIPIP, c.recvProfile == SpoofProfileProto58:
			inner, ok = payload, true // proto 4 / 58: the whole IP body is ours
		case c.recvProfile == SpoofProfileGRE:
			inner, ok = stripGREShim(payload)
		default: // tcp
			inner, ok = stripSpoofShim(c.recvProfile, c.port, payload)
		}
		if !ok {
			continue
		}
		inner, ok = c.undoDPI(c.recvProfile, inner)
		if !ok || len(inner) == 0 {
			continue
		}
		return copy(p, inner), peer, nil
	}
}

// undoDPI reverses the obfuscation WriteTo applied, in mirror order: the fake
// TLS record header (tcp only) first, then the self-describing padding. A frame
// that does not carry what the config says it should is rejected, so a mismatch
// between the two ends fails closed rather than feeding the encryption garbage.
func (c *spoofConn) undoDPI(profile SpoofProfile, inner []byte) ([]byte, bool) {
	if c.dpi.FakeTLS && profile == SpoofProfileTCP {
		var ok bool
		if inner, ok = stripFakeTLS(inner); !ok {
			return nil, false
		}
	}
	if c.dpi.Padding {
		var ok bool
		if inner, ok = stripPadding(inner); !ok {
			return nil, false
		}
	}
	return inner, true
}

func (c *spoofConn) Close() error {
	if c.rst != nil {
		c.rst.remove()
	}
	if c.icmpEcho {
		releaseICMPEchoSuppression()
		c.icmpEcho = false
	}
	c.send.Close()
	if c.xdp != nil {
		return c.xdp.Close()
	}
	if c.udpRecv != nil {
		return c.udpRecv.Close()
	}
	return c.rawRecv.Close()
}

// spoofDiag reports whether the XDP fast path is in use, for the tunnel log —
// the mirror of pck's PckDiag. Empty when no NIC was configured for XDP.
func (c *spoofConn) spoofDiag() string { return c.xdpNote }

func (c *spoofConn) LocalAddr() net.Addr {
	if c.xdp != nil {
		// No receive socket in XDP mode; the send socket's local address stands in.
		return c.sendPC.LocalAddr()
	}
	if c.udpRecv != nil {
		return c.udpRecv.LocalAddr()
	}
	return c.rawRecvPC.LocalAddr()
}

// In XDP mode the read deadline is the ring reader's, and the write deadline the
// send socket's; there is no single receive socket that carries both.
func (c *spoofConn) SetDeadline(t time.Time) error {
	if c.xdp != nil {
		c.xdp.setReadDeadline(t)
		return c.send.SetWriteDeadline(t)
	}
	if c.udpRecv != nil {
		return c.udpRecv.SetDeadline(t)
	}
	return c.rawRecv.SetDeadline(t)
}

func (c *spoofConn) SetReadDeadline(t time.Time) error {
	if c.xdp != nil {
		c.xdp.setReadDeadline(t)
		return nil
	}
	if c.udpRecv != nil {
		return c.udpRecv.SetReadDeadline(t)
	}
	return c.rawRecv.SetReadDeadline(t)
}

func (c *spoofConn) SetWriteDeadline(t time.Time) error {
	if c.xdp != nil {
		return c.send.SetWriteDeadline(t)
	}
	if c.udpRecv != nil {
		return c.udpRecv.SetWriteDeadline(t)
	}
	return c.rawRecv.SetWriteDeadline(t)
}
