package network

import "net"

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
// so this comfortably covers a normal frame; a larger one is truncated and
// dropped by the strip step above, which the reliable layer (KCP) or the inner
// transport (WireGuard, in relay mode) then recovers.
const maxXDPPayload = 2048
