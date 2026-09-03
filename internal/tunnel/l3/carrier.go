package l3

import (
	"fmt"
	"net"
	"strings"
	"sync"
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
	// CarrierQuic is a real QUIC connection carrying the tunnel in DATAGRAM
	// frames — unreliable, so it is a carrier and not a transport. See
	// carrier_quic.go.
	CarrierQuic = "quic"
)

// KnownCarrier reports whether a carrier name is one this engine can open.
//
// Exported so the screens that offer carriers can be checked against the engine
// that has to open them, rather than against a second list written by hand.
func KnownCarrier(name string) bool { return knownCarrier(name) }

// knownCarrier reports whether a name can be opened, so a configuration can be
// refused before anything is created rather than after.
func knownCarrier(name string) bool {
	switch name {
	case CarrierUDP, CarrierPck, CarrierXdi, CarrierSpoof, CarrierQuic:
		return true
	}
	return false
}

// openCarrier builds the carrier named in the config. The returned address is
// the peer to send to, or nil on the listening side of a carrier that learns
// its peer from the packets that arrive.
func openCarrier(cfg Config) (DatagramCarrier, net.Addr, error) {
	carrier, peer, err := openBareCarrier(cfg)
	if err != nil {
		return nil, nil, err
	}
	// Error correction wraps whichever carrier was opened, so the scheme is the
	// same over udp, spoof, pck and xdi and the MTU calculation picks up its
	// cost through Overhead(). Disabled, this hands the carrier straight back.
	wrapped, err := newFECCarrier(carrier, cfg.FEC)
	if err != nil {
		carrier.Close()
		return nil, nil, err
	}
	return wrapped, peer, nil
}

// openBareCarrier builds the carrier the config names, without the layers that
// wrap it.
func openBareCarrier(cfg Config) (DatagramCarrier, net.Addr, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Carrier)) {
	case "", CarrierUDP:
		return openUDPPaths(cfg)
	case CarrierPck:
		return openPck(cfg)
	case CarrierXdi:
		return openXdi(cfg)
	case CarrierSpoof:
		return openSpoof(cfg)
	case CarrierQuic:
		return openQuic(cfg)
	default:
		// Refusing by name is better than accepting one and quietly running it
		// over UDP, which would look like it worked until the path filtered it.
		return nil, nil, fmt.Errorf(
			"l3: carrier %q is not available (have %q, %q, %q, %q, %q)",
			cfg.Carrier, CarrierUDP, CarrierPck, CarrierXdi, CarrierSpoof, CarrierQuic)
	}
}

// openUDPPaths opens the plain UDP carrier — one socket, or several spread over
// consecutive ports when the configuration asks for them. See multipath.go for
// why several, and why only this carrier gets the option.
func openUDPPaths(cfg Config) (DatagramCarrier, net.Addr, error) {
	n := cfg.Multipath.Paths
	if n < 1 {
		n = 1
	}
	if cfg.Mode == ModeDial {
		paths := make([]DatagramCarrier, 0, n)
		var first net.Addr
		for i := 0; i < n; i++ {
			addr, err := pathAddr(cfg.Addr, i)
			if err != nil {
				closeAll(paths)
				return nil, nil, err
			}
			c, peer, err := dialUDP(addr, cfg.SockBuf)
			if err != nil {
				closeAll(paths)
				return nil, nil, err
			}
			// Each path sends to its own port; a connected sub-carrier keeps
			// that itself, so nothing above has to route between them.
			paths = append(paths, &pinnedCarrier{DatagramCarrier: c, peer: peer})
			if i == 0 {
				first = peer
			}
		}
		return newMultipathCarrier(paths, first), first, nil
	}

	paths := make([]DatagramCarrier, 0, n)
	for i := 0; i < n; i++ {
		addr, err := pathAddr(cfg.Addr, i)
		if err != nil {
			closeAll(paths)
			return nil, nil, err
		}
		c, err := listenUDP(addr, cfg.SockBuf)
		if err != nil {
			closeAll(paths)
			return nil, nil, err
		}
		// The listening side learns each path's peer from what arrives on it.
		paths = append(paths, &pinnedCarrier{DatagramCarrier: c})
	}
	return newMultipathCarrier(paths, nil), nil, nil
}

// pinnedCarrier remembers the address one path talks to, so the multipath layer
// above can hand it a datagram without saying where it goes.
//
// The dialling side is given its peer up front. The listening side learns it
// from the first datagram that arrives on that path and answers there, which is
// also how it follows a peer whose address changes — per path, without the
// tunnel above seeing an address move.
type pinnedCarrier struct {
	DatagramCarrier
	mu   sync.Mutex
	peer net.Addr
}

func (c *pinnedCarrier) WriteTo(p []byte, addr net.Addr) (int, error) {
	c.mu.Lock()
	dst := c.peer
	c.mu.Unlock()
	if dst == nil {
		dst = addr
	}
	if dst == nil {
		// Nothing has arrived on this path yet and nobody said where to send:
		// dropping is right, and the other paths carry the tunnel meanwhile.
		return len(p), nil
	}
	return c.DatagramCarrier.WriteTo(p, dst)
}

func (c *pinnedCarrier) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.DatagramCarrier.ReadFrom(p)
	if err == nil && addr != nil {
		c.mu.Lock()
		c.peer = addr
		c.mu.Unlock()
	}
	return n, addr, err
}

// closeAll shuts the paths opened so far, for the error paths above.
func closeAll(paths []DatagramCarrier) {
	for _, p := range paths {
		p.Close()
	}
}
