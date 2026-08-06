//go:build linux

package network

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync/atomic"

	"golang.org/x/net/ipv4"
)

// SpoofRawSender sends individual UDP datagrams with a forged source address. It
// is the send half of the spoof carrier, exposed on its own for the
// spoof-capability tester, which needs to emit probes from many different forged
// sources without standing up a whole tunnel.
//
// Linux only, and needs a raw socket (root or CAP_NET_RAW).
type SpoofRawSender struct {
	send   *ipv4.RawConn
	sendPC net.PacketConn
	ipID   atomic.Uint32
}

// NewSpoofRawSender opens the raw send socket, optionally pinned to an interface.
func NewSpoofRawSender(iface string) (*SpoofRawSender, error) {
	pc, err := net.ListenPacket("ip4:17", "0.0.0.0")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("the spoof tester needs a raw IP socket, which requires root or CAP_NET_RAW: %w", err)
		}
		return nil, fmt.Errorf("spoof: could not open the raw send socket: %w", err)
	}
	if iface != "" {
		if err := bindPacketConnToInterface(pc, iface); err != nil {
			pc.Close()
			return nil, fmt.Errorf("spoof: could not bind the raw socket to %q: %w", iface, err)
		}
	}
	raw, err := ipv4.NewRawConn(pc)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("spoof: could not take control of the IP header: %w", err)
	}
	return &SpoofRawSender{send: raw, sendPC: pc}, nil
}

// SendUDP sends one UDP datagram from the forged source to dst, carrying payload.
func (s *SpoofRawSender) SendUDP(src, dst net.IP, srcPort, dstPort uint16, payload []byte) error {
	src4, dst4 := src.To4(), dst.To4()
	if src4 == nil || dst4 == nil {
		return fmt.Errorf("spoof: SendUDP requires IPv4 addresses")
	}
	shim := buildUDPShimPorts(srcPort, dstPort, src4, dst4, payload)
	h := &ipv4.Header{
		Version:  ipv4.Version,
		Len:      ipv4.HeaderLen,
		TotalLen: ipv4.HeaderLen + len(shim),
		ID:       int(s.ipID.Add(1) & 0xffff),
		Flags:    ipv4.DontFragment,
		TTL:      64,
		Protocol: 17,
		Src:      src4,
		Dst:      dst4,
	}
	return s.send.WriteTo(h, shim, nil)
}

func (s *SpoofRawSender) Close() error { return s.send.Close() }
