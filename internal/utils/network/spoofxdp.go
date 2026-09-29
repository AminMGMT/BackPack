package network

import (
	"net"

	"golang.org/x/net/ipv4"
)

// spoofXDPConfig is what one XDP receiver needs, gathered so the Linux
// constructor and the non-Linux stub share one signature. Built by the carrier
// from the same identity it filters the ordinary receive on, so the XDP fast
// path and the fallback accept exactly the same packets.
type spoofXDPConfig struct {
	iface   string // NIC to attach the XDP program to
	proto   int    // IP protocol number the profile rides on (17, 6, 1, 58, 4, 47)
	port    uint16 // the demux port/identifier, matched in-kernel when portOff >= 0
	portOff int    // byte offset of the match field within the L4 header:
	// 2 = udp/tcp destination port, 4 = icmp/icmpv6 echo id, -1 = none
	expectSrc net.IP // required forged source (4-byte), nil = accept any
	sockBuf   int    // ring buffer sizing hint
}

// maxXDPPayload is the largest L4 segment the XDP program copies into the ring
// buffer per packet. Datagrams the carrier emits are sized under the tunnel MTU,
// so this comfortably covers a normal frame. A larger segment, and any IP
// fragment, is passed to the kernel instead — where, with XDP attached, no
// socket is open to read it — so a carrier whose spoof_mtu allows larger
// segments does not use XDP at all (xdpCanCarry).
const maxXDPPayload = 2048

// xdpCanCarry reports whether every packet a carrier with this spoof_mtu emits
// fits what the XDP program captures. A larger one would be passed to a kernel
// stack that, with XDP attached, has no socket open to read it.
func xdpCanCarry(mtu int) bool { return mtu-ipv4.HeaderLen <= maxXDPPayload }
