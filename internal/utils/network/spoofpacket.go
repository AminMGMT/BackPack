package network

import (
	"encoding/binary"
	"net"
)

// The L4 shim the spoof carrier wraps around each datagram, and its checksum.
//
// This is deliberately kept out of the Linux-only socket file so it can be unit
// tested on any platform: it is pure byte manipulation with no privilege and no
// syscall. The raw socket in spoofconn_linux.go calls into it; nothing here
// touches the network.

// buildSpoofShim wraps framed in the profile's L4 header (UDP or TCP), stamping
// port as both source and destination and computing the checksum over the given
// addresses. seq is used only by the tcp profile, as its sequence number.
func buildSpoofShim(profile SpoofProfile, port uint16, seq uint32, src, dst net.IP, framed []byte) []byte {
	if profile == SpoofProfileTCP {
		return buildTCPShim(port, seq, src, dst, framed)
	}
	return buildUDPShim(port, src, dst, framed)
}

func buildUDPShim(port uint16, src, dst net.IP, framed []byte) []byte {
	length := 8 + len(framed)
	b := make([]byte, length)
	binary.BigEndian.PutUint16(b[0:2], port) // source port
	binary.BigEndian.PutUint16(b[2:4], port) // dest port
	binary.BigEndian.PutUint16(b[4:6], uint16(length))
	copy(b[8:], framed)
	csum := l4Checksum(src, dst, 17, b)
	// A UDP checksum of 0 means "none"; a real 0 is transmitted as 0xFFFF so it
	// is never mistaken for that.
	if csum == 0 {
		csum = 0xffff
	}
	binary.BigEndian.PutUint16(b[6:8], csum)
	return b
}

func buildTCPShim(port uint16, seq uint32, src, dst net.IP, framed []byte) []byte {
	length := 20 + len(framed)
	b := make([]byte, length)
	binary.BigEndian.PutUint16(b[0:2], port) // source port
	binary.BigEndian.PutUint16(b[2:4], port) // dest port
	binary.BigEndian.PutUint32(b[4:8], seq)  // seq
	binary.BigEndian.PutUint32(b[8:12], 0)   // ack
	b[12] = 5 << 4                           // data offset = 5 words, no options
	b[13] = 0x18                             // PSH | ACK, so it reads as established traffic
	binary.BigEndian.PutUint16(b[14:16], 0xffff)
	copy(b[20:], framed)
	csum := l4Checksum(src, dst, 6, b)
	binary.BigEndian.PutUint16(b[16:18], csum)
	return b
}

// stripSpoofShim validates the L4 header of an incoming packet and returns the
// framed payload inside, or ok=false if it is not addressed to port or is too
// short to hold the header.
func stripSpoofShim(profile SpoofProfile, port uint16, l4 []byte) ([]byte, bool) {
	if profile == SpoofProfileTCP {
		if len(l4) < 20 || binary.BigEndian.Uint16(l4[2:4]) != port {
			return nil, false
		}
		off := int(l4[12]>>4) * 4
		if off < 20 || off > len(l4) {
			return nil, false
		}
		return l4[off:], true
	}
	if len(l4) < 8 || binary.BigEndian.Uint16(l4[2:4]) != port {
		return nil, false
	}
	return l4[8:], true
}

// l4Checksum computes the 16-bit ones-complement checksum of an L4 segment over
// its IPv4 pseudo-header, as UDP and TCP both require. The checksum field within
// the segment must be zero when this is called.
func l4Checksum(src, dst net.IP, proto int, segment []byte) uint16 {
	var sum uint32
	add := func(b []byte) {
		for i := 0; i+1 < len(b); i += 2 {
			sum += uint32(b[i])<<8 | uint32(b[i+1])
		}
		if len(b)%2 == 1 {
			sum += uint32(b[len(b)-1]) << 8
		}
	}
	pseudo := make([]byte, 12)
	copy(pseudo[0:4], src.To4())
	copy(pseudo[4:8], dst.To4())
	pseudo[9] = byte(proto)
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(segment)))
	add(pseudo)
	add(segment)
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
