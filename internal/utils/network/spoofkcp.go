package network

import (
	"fmt"
	"io"
	"net"

	kcp "github.com/xtaci/kcp-go/v5"
)

// This file keeps everything the IP-spoofing carrier needs off the shared KCP
// path. KCPDial/KCPListen only dispatch into the helpers here when a spoof
// carrier is configured, so the plain-UDP, xdi and pck paths cannot be touched
// by a change to the spoof transport — and vice versa. If the carrier ever
// borrows more from the KCP or UDP machinery, it is copied down here rather than
// reached for in the shared file, so editing spoof never risks the udp tunnel.

// SpoofCarrier is the IP-spoofing carrier's tuning, passed down to the raw
// socket. Nil in KCPSettings means the session rides on UDP (or ICMP) instead.
//
// Uplink is the client→server profile, Downlink the server→client one; for a
// symmetric tunnel they are equal. The carrier's own constructor picks which is
// its send and which its receive from the side it is on.
type SpoofCarrier struct {
	Uplink     SpoofProfile
	Downlink   SpoofProfile
	SrcIP      string   // forged source address, empty to keep the real one
	SrcPool    []string // forged sources to rotate through; SrcIP is a member
	PeerIP     string   // peer's real IPv4; required on the server, derived on the client
	Interface  string   // egress device to pin the raw socket to, empty for none
	XDPIface   string   // NIC to attach the XDP receive fast path to, empty = off
	SockBuf    int      // SO_SNDBUF/SO_RCVBUF for the carrier's sockets, 0 = default
	PeerSrcIP  string   // expected forged source of inbound packets, empty = accept any
	ReplySplit bool     // icmp/icmpv6: client sends Echo Request, server sends Echo Reply
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
// back — so it lands in the tunnel log the same way pck's diagnostics do.
type spoofDiagnoser interface{ spoofDiag() string }

// logSpoofDiag emits the carrier's startup note through Logf, if both are set.
func logSpoofDiag(conn interface{}, logf func(string, ...interface{})) {
	if logf == nil {
		return
	}
	if d, ok := conn.(spoofDiagnoser); ok {
		if note := d.spoofDiag(); note != "" {
			logf("%s", note)
		}
	}
}

// spoofMaxOverhead is the header cost KCP must budget for on a spoof carrier:
// the larger of the two directions' overhead, so a datagram never fragments
// whichever way it goes. effectiveMTU subtracts this from the configured MTU.
func spoofMaxOverhead(s KCPSettings) int {
	over := spoofOverhead(s.Spoof.Uplink)
	if d := spoofOverhead(s.Spoof.Downlink); d > over {
		over = d
	}
	return over
}

// spoofKCPListen builds the server side of a KCP session carried over the spoof
// transport: the send side is a raw IPv4 socket and the receive side an ordinary
// UDP one, handed to KCP as one PacketConn. The server must be told the client's
// real address, because the forged packets cannot reveal it. Called only from
// KCPListen when s.Spoof != nil.
func spoofKCPListen(token string, block kcp.BlockCrypt, s KCPSettings) (*kcp.Listener, io.Closer, error) {
	peer := net.ParseIP(s.Spoof.PeerIP)
	if peer == nil {
		return nil, nil, fmt.Errorf("spoof: spoof_peer_ip must be set to the client's real IPv4 address on the server")
	}
	conn, err := newSpoofServerConn(s.Spoof.spoofOpts(token, peer))
	if err != nil {
		return nil, nil, err
	}
	logSpoofDiag(conn, s.Logf)
	listener, err := kcp.ServeConn(block, s.DataShards, s.ParityShards, conn)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("spoof: failed to start the KCP listener: %w", err)
	}
	return listener, conn, nil
}

// spoofKCPDial builds the client side of a KCP session over the spoof transport.
// The real destination every packet is routed to is the server: its configured
// real address if given, otherwise the host of remoteAddr. The returned session
// has no tuning applied yet — KCPDial applies it, the same as for every other
// carrier. Called only from KCPDial when s.Spoof != nil.
func spoofKCPDial(remoteAddr, token string, block kcp.BlockCrypt, s KCPSettings) (*kcp.UDPSession, error) {
	ipAddr, err := hostToIPAddr(remoteAddr)
	if err != nil {
		return nil, err
	}
	peer := ipAddr.IP
	if s.Spoof.PeerIP != "" {
		if p := net.ParseIP(s.Spoof.PeerIP); p != nil {
			peer = p
		}
	}
	conn, err := newSpoofClientConn(s.Spoof.spoofOpts(token, peer))
	if err != nil {
		return nil, err
	}
	logSpoofDiag(conn, s.Logf)
	session, err := ownedKCPSession(&net.IPAddr{IP: peer}, block, s.DataShards, s.ParityShards, conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("spoof: failed to open the KCP session: %w", err)
	}
	return session, nil
}
