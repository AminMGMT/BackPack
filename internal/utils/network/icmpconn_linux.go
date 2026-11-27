//go:build linux

package network

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
)

// A net.PacketConn that carries datagrams inside ICMP echo.
//
// This is the whole of what makes the xdi transport different from kcp: KCP,
// its forward error correction and its encryption all sit on top unchanged,
// because to them this is just a net.PacketConn like the UDP socket they
// usually get. What it does underneath is wrap each outgoing datagram in an
// ICMP echo — a request from the client, a reply from the server — and unwrap
// each incoming one, dropping everything that is not this tunnel's inbound
// traffic (see icmpframe.go).
//
// It exists for the one network where UDP and TCP are filtered but ICMP is
// not — the tunnel rides in ping packets, which such a network is unwilling to
// drop because ping is how it proves itself reachable. It is experimental and
// costs a raw socket (root, or CAP_NET_RAW), so it is never a default.

// icmpSocket is the part of *icmp.PacketConn this carrier uses.
//
// It is an interface for one reason: a raw ICMP socket needs privilege, so
// nothing that opened one could be tested, and the fault that stopped xdi
// working — every session of a tunnel looking identical on the wire — lived
// for releases behind that. With the socket injectable the whole carrier runs
// against an in-memory wire, kcp-go and all. See icmpconn_linux_test.go.
type icmpSocket interface {
	ReadFrom(b []byte) (int, net.Addr, error)
	WriteTo(b []byte, dst net.Addr) (int, error)
	Close() error
	LocalAddr() net.Addr
	SetDeadline(t time.Time) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
}

// icmpConn is the packet connection. One is opened per KCP session — the
// control channel and each pool connection get their own — and several on one
// host coexist because each reads only packets carrying its own tunnel's
// session tag, and, on the client, its own echo identifier.
type icmpConn struct {
	pc     icmpSocket
	proto  int  // ProtocolICMP, for parsing
	server bool // server sends replies and reads requests; client the reverse
	// v6 is an ICMPv6 carrier: echo types 128 and 129 instead of 8 and 0, and
	// the checksum is the kernel's to fill in. See icmpTypes.
	v6 bool
	// id is the echo identifier this carrier answers to. On the client it names
	// this one session and is drawn per socket; on the server it is unused,
	// because one socket serves every session and each packet carries the
	// identifier of the session it belongs to.
	id        uint16
	tag       [xdiTagLen]byte
	seq       atomic.Uint32
	closeOnce sync.Once
	// echoGuard keeps the server's kernel from answering the client's
	// requests; nil on the client. See installXdiEchoGuard.
	echoGuard *icmpEchoGuard
	// accept lets this tunnel's echoes through a firewall that drops ICMP.
	// See installXdiAcceptRule.
	accept *icmpEchoGuard
	// batch is recvmmsg's and sendmmsg's state; see icmpbatch_linux.go.
	batch icmpBatch
	// foreign is told about a client echo carrying another token's tag; see
	// SetForeignHook.
	foreign atomic.Pointer[func(net.Addr)]
}

// IPv6 reports whether this carrier runs over ICMPv6, whose larger IP header
// a tunnel inside it has to leave room for.
func (c *icmpConn) IPv6() bool { return c.v6 }

// SetForeignHook asks the server to report client echoes that carry another
// tunnel's tag — the only trace a token copied wrong leaves, since such a
// packet never reaches the handshake.
func (c *icmpConn) SetForeignHook(f func(net.Addr)) { c.foreign.Store(&f) }

// noteForeign reports data that is a client's xdi echo for a different tunnel.
func (c *icmpConn) noteForeign(data []byte, from net.Addr) {
	if !c.server || len(data) < xdiHeaderLen || data[xdiTagLen] != xdiDirClient {
		return
	}
	if h := c.foreign.Load(); h != nil {
		(*h)(from)
	}
}

// icmpMTUOverhead is what the ICMP framing costs on top of the IP header, so
// the KCP layer can be told a small enough MTU that a datagram never fragments:
// the 8-byte ICMP echo header plus this tunnel's own tag-and-direction prefix.
const icmpMTUOverhead = 8 + xdiHeaderLen

// xdiSockBuf is the raw ICMP socket's buffers when the configuration names
// none: what pck's packet socket gets, for the same reason.
const xdiSockBuf = 8 << 20

// listenICMP opens the raw ICMP socket both ends share. It needs privilege, and
// says so plainly when it does not have it rather than failing obscurely later.
// sockBuf sizes its buffers; 0 takes xdiSockBuf.
func listenICMP(sockBuf int, v6 bool) (*icmp.PacketConn, error) {
	network, addr := "ip4:icmp", "0.0.0.0"
	if v6 {
		network, addr = "ip6:ipv6-icmp", "::"
	}
	pc, err := icmp.ListenPacket(network, addr)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("xdi needs a raw ICMP socket, which requires root or CAP_NET_RAW: %w", err)
		}
		return nil, fmt.Errorf("xdi: could not open the ICMP socket: %w", err)
	}
	sizeICMPBuffers(pc, sockBuf)
	return pc, nil
}

// sizeICMPBuffers gives the raw socket room for a burst. It used to keep the
// kernel default (rmem_default, about 200 KB), and the preset's socket buffer
// never reached it: on a 70 ms path a loaded tunnel overflowed it, the kernel
// dropped the echoes, and the TCP inside read that as congestion — 250 Mbit/s
// each way where the same path carried 4.9 Gbit/s over GRE. The FORCE options
// pass net.core.rmem_max and wmem_max, which the raw socket's privilege
// allows; where they are refused the plain ones take what the limit permits.
func sizeICMPBuffers(pc *icmp.PacketConn, sockBuf int) {
	if sockBuf <= 0 {
		sockBuf = xdiSockBuf
	}
	icmpSocketControl(pc, func(fd int) {
		for _, o := range [][2]int{{unix.SO_RCVBUFFORCE, unix.SO_RCVBUF}, {unix.SO_SNDBUFFORCE, unix.SO_SNDBUF}} {
			if unix.SetsockoptInt(fd, unix.SOL_SOCKET, o[0], sockBuf) != nil {
				_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, o[1], sockBuf)
			}
		}
	})
}

// icmpSocketControl runs f on the raw socket under pc.
func icmpSocketControl(pc *icmp.PacketConn, f func(fd int)) {
	var under net.PacketConn
	if p4 := pc.IPv4PacketConn(); p4 != nil {
		under = p4.PacketConn
	} else if p6 := pc.IPv6PacketConn(); p6 != nil {
		under = p6.PacketConn
	}
	sc, ok := under.(syscall.Conn)
	if !ok {
		return
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) { f(int(fd)) })
}

// sendWhole makes the carrier send each packet whole or not at all: the
// don't-fragment bit on every packet, and a packet larger than the interface
// refused here (EMSGSIZE) instead of split in two by this host.
//
// The layer-3 tunnel needs it to measure the path (mtuprobe.go). A raw socket
// otherwise fragments whatever does not fit, so every probe size came back
// answered: on a 1400-byte path the tunnel concluded it carried 1426 bytes
// over ICMP and 1406 over ICMPv6, and every full-size packet after that went
// out as two fragments — twice the packets, and a whole packet lost with
// either half. Over ICMPv6 with FEC on two CPUs a speed test's streams went
// silent. The probe now sees the refusal and settles on what fits.
//
// Not for KCP over xdi: kcp-go ends the session on any write error, so there
// a kcp_mtu too large for the path is left to fragment as before.
func sendWhole(conn net.PacketConn) {
	c, ok := conn.(*icmpConn)
	if !ok {
		return
	}
	pc, ok := c.pc.(*icmp.PacketConn)
	if !ok {
		return
	}
	icmpSocketControl(pc, func(fd int) {
		if c.v6 {
			_ = unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_MTU_DISCOVER, unix.IPV6_PMTUDISC_PROBE)
		} else {
			_ = unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_MTU_DISCOVER, unix.IP_PMTUDISC_PROBE)
		}
	})
}

// newICMPServerConn opens the server side: it will read echo requests and
// answer with echo replies.
func newICMPServerConn(token string, sockBuf int, v6 bool) (net.PacketConn, error) {
	pc, err := listenICMP(sockBuf, v6)
	if err != nil {
		return nil, err
	}
	c := newICMPServerConnWith(pc, token).(*icmpConn)
	c.v6 = v6
	request, _ := c.icmpTypes()
	attachICMPFilter(pc, request, -1, v6)
	c.echoGuard = installXdiEchoGuard(c.tag, v6)
	c.accept = installXdiAcceptRule(c.tag, true, v6)
	return c, nil
}

// newICMPServerConnWith is the constructor the tests use, with the socket
// supplied rather than opened.
func newICMPServerConnWith(pc icmpSocket, token string) net.PacketConn {
	return &icmpConn{pc: pc, proto: ipv4ICMPProtocol, server: true, tag: xdiTag(token)}
}

// newICMPClientConn opens the client side: it will send echo requests and read
// the replies.
func newICMPClientConn(token string, sockBuf int, v6 bool) (net.PacketConn, error) {
	pc, err := listenICMP(sockBuf, v6)
	if err != nil {
		return nil, err
	}
	c := newICMPClientConnWith(pc, token).(*icmpConn)
	c.v6 = v6
	_, reply := c.icmpTypes()
	attachICMPFilter(pc, reply, int(c.id), v6)
	c.accept = installXdiAcceptRule(c.tag, false, v6)
	return c, nil
}

// newICMPClientConnWith is the constructor the tests use, with the socket
// supplied rather than opened. Each call takes an identifier of its own — that
// is what makes this session distinguishable from the tunnel's others.
func newICMPClientConnWith(pc icmpSocket, token string) net.PacketConn {
	return &icmpConn{
		pc:    pc,
		proto: ipv4ICMPProtocol,
		id:    acquireXdiSessionID(),
		tag:   xdiTag(token),
	}
}

// ipv4ICMPProtocol is the IANA protocol number for ICMP, which icmp.ParseMessage
// wants in order to parse an IPv4 ICMP message. Named rather than the bare 1 so
// the read path reads as what it is.
const ipv4ICMPProtocol = 1

// WriteTo wraps a KCP datagram in an ICMP echo addressed to dst and sends it.
//
// The echo is a request from the client and a reply from the server, so that
// the pair looks on the wire exactly like a ping and its answer.
func (c *icmpConn) WriteTo(p []byte, dst net.Addr) (int, error) {
	ipAddr, err := toIPAddr(dst)
	if err != nil {
		return 0, err
	}

	request, reply := c.icmpTypes()
	typ := request
	if c.server {
		typ = reply
	}
	// The echo is built straight into one pooled buffer: header, tag,
	// direction, payload, then the checksum over all of it. It used to go
	// through icmp.Message.Marshal, which allocated the message, its body and
	// a fresh output slice and copied the payload a second time — for every
	// datagram the tunnel sends.
	wp := xdiBuffers.Get().(*[]byte)
	defer xdiBuffers.Put(wp)
	wire := appendEcho((*wp)[:0], typ, uint16(c.echoIDFor(dst)), uint16(c.seq.Add(1)))
	wire = appendXdiPayload(wire, c.tag, outboundDir(c.server), p)
	c.checksum(wire)
	if _, err := c.pc.WriteTo(wire, ipAddr); err != nil {
		if droppedOnTheWayOut(err) {
			return len(p), nil
		}
		return 0, err
	}
	// Report the caller's byte count, not the wire count: KCP measures what it
	// asked to send, and the framing is ours to account for, not its concern.
	return len(p), nil
}

// droppedOnTheWayOut reports an error that means only that this one packet
// was dropped on its way out: ENOBUFS, from an interface queue that was full.
//
// A raw IPv4 socket never reports it — the kernel turns it into success unless
// IP_RECVERR is set — but a raw IPv6 one does, and kcp-go takes any write error
// as the end of the session. Over ICMPv6 a tunnel whose queue overflowed once
// under load stopped carrying anything: a speed test's streams went silent
// while the echoes still passed. It is a lost packet, as it is over IPv4, and
// KCP resends it.
func droppedOnTheWayOut(err error) bool { return errors.Is(err, syscall.ENOBUFS) }

// ReadFrom returns the next datagram addressed to this tunnel, dropping ICMP
// traffic that belongs to another tunnel, to a bare ping, or to the kernel's
// automatic reply. It respects the read deadline across those drops.
func (c *icmpConn) ReadFrom(p []byte) (int, net.Addr, error) {
	// Pooled, not allocated. This is called once per received datagram, and a
	// fresh multi-kilobyte slice each time is the garbage collector setting the
	// tunnel's pace rather than the network.
	bp := xdiBuffers.Get().(*[]byte)
	defer xdiBuffers.Put(bp)
	buf := *bp
	request, reply := c.icmpTypes()
	wantType := request // the server reads requests
	if !c.server {
		wantType = reply // the client reads replies
	}
	wantDir := inboundDir(c.server)

	for {
		n, peer, err := c.pc.ReadFrom(buf)
		if err != nil {
			return 0, nil, err
		}
		// The type, and on the client the echo identifier below. The socket
		// filter normally keeps everything else away already; this is what is
		// left where it could not be attached.
		if n < 8 || buf[0] != wantType {
			continue
		}
		// An echo is eight bytes of header and then its data; nothing in it
		// needs icmp.ParseMessage, which allocated the message and copied the
		// data out for every datagram received.
		echoID := int(buf[4])<<8 | int(buf[5])
		// The client answers to one identifier, its own, which is how it
		// ignores the packets of the tunnel's other sessions — every raw ICMP
		// socket on the host sees all of them. The server answers to every
		// identifier, because each is one of its clients' sessions; what says a
		// packet is this tunnel's at all is the tag and the direction below.
		if !c.server && echoID != int(c.id) {
			continue
		}
		payload, ok := decodeXdiPayload(c.tag, wantDir, buf[8:n])
		if !ok {
			c.noteForeign(buf[8:n], peer)
			continue
		}
		return copy(p, payload), c.peerAddr(peer, echoID), nil
	}
}

// Close releases the socket and, on the client, the identifier this session
// held, so a long-running process that opens and drops sessions does not
// exhaust the space.
func (c *icmpConn) Close() error {
	c.closeOnce.Do(func() {
		if !c.server {
			releaseXdiSessionID(c.id)
		}
		c.echoGuard.remove()
		c.accept.remove()
	})
	return c.pc.Close()
}

// echoIDFor is the identifier to stamp on an outgoing packet.
//
// The client has one session and one identifier. The server has one socket and
// many sessions, so it reads the identifier back out of the address KCP is
// replying to — which is the address peerAddr put it into when the request
// arrived.
func (c *icmpConn) echoIDFor(dst net.Addr) int {
	if !c.server {
		return int(c.id)
	}
	if udp, ok := dst.(*net.UDPAddr); ok {
		return udp.Port
	}
	// An address with no identifier in it is one this carrier did not report,
	// so there is no session to answer. Zero is the identifier no session is
	// ever given, which makes such a packet ignorable rather than ambiguous.
	return 0
}

// peerAddr is the address a received packet is reported to KCP as.
//
// On the server it carries the sender's echo identifier in the port field. That
// is not cosmetic: kcp-go's listener keys its sessions on this string, and
// without the identifier every session of a client collapses onto one entry and
// closes the one before it. See the note at the top of icmpframe.go.
//
// On the client it is passed through untouched, because the session was dialled
// against a bare IP and kcp-go drops anything whose address does not match the
// one it was given.
func (c *icmpConn) peerAddr(peer net.Addr, echoID int) net.Addr {
	if !c.server {
		return peer
	}
	return &net.UDPAddr{IP: addrIP(peer), Port: echoID}
}

// addrIP pulls the IP out of whatever the socket reported, through the same
// coercion the send path uses. Nil for an address that holds none, which the
// caller renders as an address with no host — visibly wrong rather than
// silently pointed somewhere else.
func addrIP(addr net.Addr) net.IP {
	if addr == nil {
		return nil
	}
	ip, err := toIPAddr(addr)
	if err != nil {
		return nil
	}
	return ip.IP
}

func (c *icmpConn) LocalAddr() net.Addr                { return c.pc.LocalAddr() }
func (c *icmpConn) SetDeadline(t time.Time) error      { return c.pc.SetDeadline(t) }
func (c *icmpConn) SetReadDeadline(t time.Time) error  { return c.pc.SetReadDeadline(t) }
func (c *icmpConn) SetWriteDeadline(t time.Time) error { return c.pc.SetWriteDeadline(t) }

// icmpEchoRequest and icmpEchoReply are the two ICMP types the tunnel uses,
// named here so the code above does not carry a bare import of ipv4 through
// every reference.
var (
	icmpEchoRequest = ipv4.ICMPTypeEcho
	icmpEchoReply   = ipv4.ICMPTypeEchoReply
)

// icmpTypes is the echo request and reply type this carrier sends and reads:
// ICMP's 8 and 0, or ICMPv6's 128 and 129.
func (c *icmpConn) icmpTypes() (request, reply byte) {
	if c.v6 {
		return byte(ipv6.ICMPTypeEchoRequest), byte(ipv6.ICMPTypeEchoReply)
	}
	return byte(icmpEchoRequest), byte(icmpEchoReply)
}

// checksum fills in an outgoing echo's checksum. ICMPv6's covers a
// pseudo-header of both addresses, which only the kernel knows at this point,
// and Linux computes it for every raw ICMPv6 socket (RFC 3542), so the field is
// left zero for it.
func (c *icmpConn) checksum(msg []byte) {
	if !c.v6 {
		setICMPChecksum(msg)
	}
}

// toIPAddr coerces whatever address KCP hands back into the *net.IPAddr the raw
// socket needs. KCP passes through exactly what ReadFrom returned, which is
// already an *net.IPAddr; the other cases are belt and braces.
func toIPAddr(addr net.Addr) (*net.IPAddr, error) {
	switch a := addr.(type) {
	case *net.IPAddr:
		return a, nil
	case *net.UDPAddr:
		return &net.IPAddr{IP: a.IP, Zone: a.Zone}, nil
	default:
		ip, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			ip = addr.String()
		}
		parsed := net.ParseIP(ip)
		if parsed == nil {
			return nil, fmt.Errorf("xdi: cannot use %q as an IP address", addr)
		}
		return &net.IPAddr{IP: parsed}, nil
	}
}
