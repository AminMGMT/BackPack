package network

import (
	"fmt"
	"net"

	"golang.org/x/net/ipv4"
)

// Bare datagram access to the obfuscated carriers.
//
// Each of the three raw carriers here — pck, xdi and spoof — is already a
// net.PacketConn underneath; the pck and xdi constructors are simply not
// exported, because until now the only caller was KCP inside this package.
// This file exports them unchanged, so the layer-3 tunnel can hand its own
// sealed datagrams to the same carriers without a KCP layer in between.
//
// Why no KCP: a layer-3 tunnel carries IP packets, and an IP packet already
// belongs to something that handles its own loss. Stacking KCP's retransmit
// timer under that one makes throughput collapse under loss rather than
// degrade. This is the same reasoning that made the spoof carrier's relay mode
// strip KCP, and these functions are the general form of what that mode did
// for spoof alone.
//
// Nothing here changes the carriers. Every function is a thin adapter over the
// same constructor the KCP path uses, so a fix to a carrier reaches both
// callers at once.

// PckOverhead already exists on both platforms; see pckconn_linux.go.

// XdiOverhead is what the ICMP-echo carrier costs: the IP header, the echo
// header, and the tag-and-direction prefix that lets several tunnels share the
// host's single raw ICMP socket.
func XdiOverhead() int { return ipv4.HeaderLen + icmpMTUOverhead }

// SpoofOverhead is what the forged-source carrier costs for a profile: the IP
// header and the profile's L4 header. It does not include the optional DPI
// padding, which the caller adds if it has turned padding on.
func SpoofOverhead(p SpoofProfile) int { return spoofOverhead(p) }

// NewPckPacketConn opens the packet-level TCP carrier as a bare
// net.PacketConn.
//
// addr is the tunnel address: the bind address on the listening side, the
// peer's on the dialling side. The returned net.Addr is where the caller
// should send, and is nil on the listening side, which learns each peer from
// the segments that arrive exactly as a UDP socket would.
func NewPckPacketConn(listening bool, token, addr string, carrier PcapCarrier) (net.PacketConn, net.Addr, error) {
	carrier.Token = token
	if carrier.Port == 0 {
		carrier.Port = portOf(addr)
	}
	if carrier.Port == 0 {
		return nil, nil, fmt.Errorf("pck: the tunnel port could not be read from %q", addr)
	}

	if listening {
		conn, err := newPckConn(true, carrier.Port, carrier)
		if err != nil {
			return nil, nil, err
		}
		return conn, nil, nil
	}

	peer, err := hostToIPAddr(addr)
	if err != nil {
		return nil, nil, err
	}
	carrier.PeerIP = peer.IP.String()
	conn, err := newPckConn(false, carrier.Port, carrier)
	if err != nil {
		return nil, nil, err
	}
	return conn, &net.UDPAddr{IP: peer.IP, Port: int(carrier.Port)}, nil
}

// NewXdiPacketConn opens the ICMP-echo carrier as a bare net.PacketConn.
//
// ICMP has no ports, so only the host of addr is used and its port is ignored;
// tunnels sharing the host's raw ICMP socket are separated by the tag derived
// from their token.
func NewXdiPacketConn(listening bool, token, addr string) (net.PacketConn, net.Addr, error) {
	if listening {
		conn, err := newICMPServerConn(token)
		if err != nil {
			return nil, nil, err
		}
		return conn, nil, nil
	}

	conn, err := newICMPClientConn(token)
	if err != nil {
		return nil, nil, err
	}
	peer, err := hostToIPAddr(addr)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, peer, nil
}
