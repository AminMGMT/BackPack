package l3

import (
	"fmt"
	"net"
	"strings"
)

// Carriers.
//
// A carrier is how a datagram reaches the peer. The tunnel above does not care
// which one it is holding: it hands over a sealed datagram and an address, and
// gets datagrams back. That indirection is what lets the same layer-3 engine
// run over plain UDP today and over the forged-source and raw-TCP carriers
// next, without the engine learning anything about them.
//
// Only datagram carriers may appear here. See the package doc for why a
// reliable carrier is not merely a poor fit but actively harmful.

// DatagramCarrier is a net.PacketConn that can also say what it costs.
type DatagramCarrier interface {
	net.PacketConn

	// Overhead is the number of bytes this carrier adds to every datagram on
	// the wire, including the outer IP header. It feeds the MTU calculation,
	// so an underestimate produces a tunnel that silently fragments.
	Overhead() int

	// CarrierName identifies the carrier in logs and diagnostics.
	CarrierName() string
}

// Byte costs of the headers a carrier puts on the wire. IPv6 is 40 rather than
// 20, which matters enough to a 1500-byte path to be worth distinguishing.
const (
	ipv4HeaderLen = 20
	ipv6HeaderLen = 40
	udpHeaderLen  = 8
)

// udpCarrier is the plain UDP carrier: no obfuscation, nothing hidden. It is
// the right choice on a path that does not filter, and it is the reference the
// other carriers are measured against.
type udpCarrier struct {
	*net.UDPConn
	overhead int
}

func (c *udpCarrier) Overhead() int       { return c.overhead }
func (c *udpCarrier) CarrierName() string { return "udp" }

// udpOverhead is the outer IP header plus the UDP header, chosen by the family
// the socket actually ended up on.
func udpOverhead(addr net.Addr) int {
	if ua, ok := addr.(*net.UDPAddr); ok && ua.IP.To4() == nil && ua.IP != nil {
		return ipv6HeaderLen + udpHeaderLen
	}
	return ipv4HeaderLen + udpHeaderLen
}

// listenUDP binds the carrier for the side that waits to be dialled.
func listenUDP(bind string, sockBuf int) (DatagramCarrier, error) {
	addr, err := net.ResolveUDPAddr("udp", bind)
	if err != nil {
		return nil, fmt.Errorf("l3: resolving the bind address %q: %w", bind, err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("l3: listening on %q: %w", bind, err)
	}
	sizeUDPBuffers(conn, sockBuf)
	return &udpCarrier{UDPConn: conn, overhead: udpOverhead(addr)}, nil
}

// dialUDP binds the carrier for the side that reaches out, and resolves the
// peer it will send to.
//
// The socket is not connected, so it can still receive from an address other
// than the one it sends to. That matters for a peer whose source address is
// not what was dialled — a NAT, or a carrier that rewrites it — and it keeps
// this path identical in shape to the listening one.
func dialUDP(remote string, sockBuf int) (DatagramCarrier, net.Addr, error) {
	peer, err := net.ResolveUDPAddr("udp", remote)
	if err != nil {
		return nil, nil, fmt.Errorf("l3: resolving the remote address %q: %w", remote, err)
	}
	local := &net.UDPAddr{IP: net.IPv4zero, Port: 0}
	if peer.IP != nil && peer.IP.To4() == nil {
		local = &net.UDPAddr{IP: net.IPv6zero, Port: 0}
	}
	conn, err := net.ListenUDP("udp", local)
	if err != nil {
		return nil, nil, fmt.Errorf("l3: opening a local socket: %w", err)
	}
	sizeUDPBuffers(conn, sockBuf)
	return &udpCarrier{UDPConn: conn, overhead: udpOverhead(peer)}, peer, nil
}

// sizeUDPBuffers asks the kernel for larger socket buffers. A burst off the
// TUN device arrives faster than the read loop drains it, and without room to
// park those datagrams the kernel drops them — which shows up as a tunnel that
// cannot reach line rate for no visible reason. Best effort: a kernel that
// refuses the size still gives a working tunnel, just a slower one.
func sizeUDPBuffers(conn *net.UDPConn, sockBuf int) {
	if sockBuf <= 0 {
		sockBuf = defaultSockBuf
	}
	_ = conn.SetReadBuffer(sockBuf)
	_ = conn.SetWriteBuffer(sockBuf)
}

// defaultSockBuf matches what the spoof carrier already asks for, for the same
// reason it does.
const defaultSockBuf = 4 << 20

// Carrier names accepted in a configuration. A reliable transport — tcp, ws,
// kcp — is deliberately absent and can never be added: see the package doc.
const (
	CarrierUDP   = "udp"
	CarrierPck   = "pck"
	CarrierXdi   = "xdi"
	CarrierSpoof = "spoof"
)

// knownCarrier reports whether a name can be opened, so a configuration can be
// refused before anything is created rather than after.
func knownCarrier(name string) bool {
	switch name {
	case CarrierUDP, CarrierPck, CarrierXdi, CarrierSpoof:
		return true
	}
	return false
}

// openCarrier builds the carrier named in the config. The returned address is
// the peer to send to, or nil on the listening side of a carrier that learns
// its peer from the packets that arrive.
func openCarrier(cfg Config) (DatagramCarrier, net.Addr, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Carrier)) {
	case "", CarrierUDP:
		if cfg.Mode == ModeDial {
			return dialUDP(cfg.Addr, cfg.SockBuf)
		}
		carrier, err := listenUDP(cfg.Addr, cfg.SockBuf)
		return carrier, nil, err
	case CarrierPck:
		return openPck(cfg)
	case CarrierXdi:
		return openXdi(cfg)
	case CarrierSpoof:
		return openSpoof(cfg)
	default:
		// Refusing by name is better than accepting one and quietly running it
		// over UDP, which would look like it worked until the path filtered it.
		return nil, nil, fmt.Errorf(
			"l3: carrier %q is not available (have %q, %q, %q, %q)",
			cfg.Carrier, CarrierUDP, CarrierPck, CarrierXdi, CarrierSpoof)
	}
}
