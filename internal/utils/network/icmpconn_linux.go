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

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
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
	// id is the echo identifier this carrier answers to. On the client it names
	// this one session and is drawn per socket; on the server it is unused,
	// because one socket serves every session and each packet carries the
	// identifier of the session it belongs to.
	id        uint16
	tag       [xdiTagLen]byte
	seq       atomic.Uint32
	closeOnce sync.Once
}

// icmpMTUOverhead is what the ICMP framing costs on top of the IP header, so
// the KCP layer can be told a small enough MTU that a datagram never fragments:
// the 8-byte ICMP echo header plus this tunnel's own tag-and-direction prefix.
const icmpMTUOverhead = 8 + xdiHeaderLen

// listenICMP opens the raw ICMP socket both ends share. It needs privilege, and
// says so plainly when it does not have it rather than failing obscurely later.
func listenICMP() (*icmp.PacketConn, error) {
	pc, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("xdi needs a raw ICMP socket, which requires root or CAP_NET_RAW: %w", err)
		}
		return nil, fmt.Errorf("xdi: could not open the ICMP socket: %w", err)
	}
	return pc, nil
}

// newICMPServerConn opens the server side: it will read echo requests and
// answer with echo replies.
func newICMPServerConn(token string) (net.PacketConn, error) {
	pc, err := listenICMP()
	if err != nil {
		return nil, err
	}
	return newICMPServerConnWith(pc, token), nil
}

// newICMPServerConnWith is the constructor the tests use, with the socket
// supplied rather than opened.
func newICMPServerConnWith(pc icmpSocket, token string) net.PacketConn {
	return &icmpConn{pc: pc, proto: ipv4ICMPProtocol, server: true, tag: xdiTag(token)}
}

// newICMPClientConn opens the client side: it will send echo requests and read
// the replies.
func newICMPClientConn(token string) (net.PacketConn, error) {
	pc, err := listenICMP()
	if err != nil {
		return nil, err
	}
	return newICMPClientConnWith(pc, token), nil
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

	typ := icmpEchoRequest
	if c.server {
		typ = icmpEchoReply
	}
	// Both buffers come from the pool: the framed payload, and the marshalled
	// echo underneath it. Each was a fresh allocation per packet, and this runs
	// once for every datagram the tunnel sends.
	fp := xdiBuffers.Get().(*[]byte)
	defer xdiBuffers.Put(fp)
	framed := appendXdiPayload(*fp, c.tag, outboundDir(c.server), p)

	body := &icmp.Echo{
		ID:   c.echoIDFor(dst),
		Seq:  int(c.seq.Add(1) & 0xffff),
		Data: framed,
	}
	msg := &icmp.Message{Type: typ, Code: 0, Body: body}
	wire, err := msg.Marshal(nil)
	if err != nil {
		return 0, err
	}
	if _, err := c.pc.WriteTo(wire, ipAddr); err != nil {
		return 0, err
	}
	// Report the caller's byte count, not the wire count: KCP measures what it
	// asked to send, and the framing is ours to account for, not its concern.
	return len(p), nil
}

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
	wantType := icmpEchoRequest // the server reads requests
	if !c.server {
		wantType = icmpEchoReply // the client reads replies
	}
	wantDir := inboundDir(c.server)

	for {
		n, peer, err := c.pc.ReadFrom(buf)
		if err != nil {
			return 0, nil, err
		}
		msg, err := icmp.ParseMessage(c.proto, buf[:n])
		if err != nil || msg.Type != wantType {
			continue
		}
		echo, ok := msg.Body.(*icmp.Echo)
		if !ok {
			continue
		}
		// The client answers to one identifier, its own, which is how it
		// ignores the packets of the tunnel's other sessions — every raw ICMP
		// socket on the host sees all of them. The server answers to every
		// identifier, because each is one of its clients' sessions; what says a
		// packet is this tunnel's at all is the tag and the direction below.
		if !c.server && echo.ID != int(c.id) {
			continue
		}
		payload, ok := decodeXdiPayload(c.tag, wantDir, echo.Data)
		if !ok {
			continue
		}
		return copy(p, payload), c.peerAddr(peer, echo.ID), nil
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
