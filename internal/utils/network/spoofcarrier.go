package network

import "net"

// The IP-spoofing carrier's shared description.
//
// The carrier itself lives in spoofconn_linux.go; this is the tuning the layer
// above hands it, kept in one place so the direct tunnel and the spoof tester
// describe it the same way.
//
// It used to sit under the reverse KCP transports as well. That could not work
// and has been withdrawn: a reverse tunnel is a control channel plus a pool of
// connections, each its own session, and a forged-source packet carries nothing
// the receiver can tell one session from another by — every one of them arrives
// at the same address. kcp-go keys its sessions on that address, so they
// collapsed onto a single entry and each new session closed the one before it.
// The direct tunnel has exactly one session, which is the shape this carrier
// can serve.

// SpoofCarrier is the IP-spoofing carrier's tuning, passed down to the raw
// socket.
//
// Uplink is the dialling→listening profile, Downlink the reverse; for a
// symmetric tunnel they are equal. The carrier's own constructor picks which is
// its send and which its receive from the side it is on.
type SpoofCarrier struct {
	Uplink     SpoofProfile
	Downlink   SpoofProfile
	SrcIP      string   // forged source address, empty to keep the real one
	SrcPool    []string // forged sources to rotate through; SrcIP is a member
	PeerIP     string   // peer's real IPv4; required when listening, derived when dialling
	Interface  string   // egress device to pin the raw socket to, empty for none
	XDPIface   string   // NIC to attach the XDP receive fast path to, empty = off
	SockBuf    int      // SO_SNDBUF/SO_RCVBUF for the carrier's sockets, 0 = default
	PeerSrcIP  string   // expected forged source of inbound packets, empty = accept any
	ReplySplit bool     // icmp/icmpv6: the dialling side sends Echo Request, the other Echo Reply
	MTU        int      // fragment sends larger than this, 0 = default 1500
	DPI        SpoofDPI // optional obfuscation knobs (ttl/dscp/port/padding/fake-tls)
}

// spoofOpts turns a carrier's public knobs into the internal constructor's
// options for one side of the tunnel.
func (c SpoofCarrier) spoofOpts(token string, realPeer net.IP) spoofConnOpts {
	return spoofConnOpts{
		token:      token,
		uplink:     c.Uplink,
		downlink:   c.Downlink,
		realPeer:   realPeer,
		srcIP:      c.SrcIP,
		srcPool:    c.SrcPool,
		iface:      c.Interface,
		xdpIface:   c.XDPIface,
		sockBuf:    c.SockBuf,
		replySplit: c.ReplySplit,
		peerSrc:    c.PeerSrcIP,
		mtu:        c.MTU,
		dpi:        c.DPI,
	}
}

// spoofDiagnoser is implemented by the spoof carrier to report a one-line
// startup note — currently whether the XDP receive fast path attached or fell
// back to the ordinary socket.
type spoofDiagnoser interface{ spoofDiag() string }

// SpoofDiag returns the carrier's startup note, or "" for anything that is not
// a spoof carrier or has nothing to say.
//
// It is worth surfacing because the XDP path declines silently by design: a
// kernel too old, or a verifier rejection, falls back rather than failing, and
// an operator who turned it on has no other way to learn which happened.
func SpoofDiag(conn net.PacketConn) string {
	if d, ok := conn.(spoofDiagnoser); ok {
		return d.spoofDiag()
	}
	return ""
}
