package network

import (
	"encoding/binary"
	"fmt"
	"net"
)

// parseSpoofPool parses every forged source the carrier may use: the whole pool
// if given, otherwise the single address, otherwise none (no spoofing). A bad
// entry is an error rather than a silent drop, so a typo is caught at startup.
// The carrier rotates through the returned addresses one per packet, which both
// spreads the tunnel's volume across them — evading a per-IP rate limit on any
// one whitelisted address — and keeps the tunnel alive if some of them start
// being dropped mid-session.
func parseSpoofPool(srcIP string, pool []string) ([]net.IP, error) {
	candidates := pool
	if len(candidates) == 0 && srcIP != "" {
		candidates = []string{srcIP}
	}
	out := make([]net.IP, 0, len(candidates))
	for _, s := range candidates {
		ip := net.ParseIP(s).To4()
		if ip == nil {
			return nil, fmt.Errorf("spoof: %q is not a valid IPv4 address", s)
		}
		out = append(out, ip)
	}
	return out, nil
}

// The L4 shim the spoof carrier wraps around each datagram, and its checksum.
//
// This is deliberately kept out of the Linux-only socket file so it can be unit
// tested on any platform: it is pure byte manipulation with no privilege and no
// syscall. The raw socket in spoofconn_linux.go calls into it; nothing here
// touches the network.

// profileL4Len is the fixed L4 header length a profile prepends: UDP 8, TCP 20,
// ICMP echo 8.
func profileL4Len(p SpoofProfile) int {
	switch p {
	case SpoofProfileTCP:
		return 20
	default: // udp and icmp both use an 8-byte header
		return 8
	}
}

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
	return buildUDPShimPorts(port, port, src, dst, framed)
}

// buildUDPShimPorts is buildUDPShim with independent source and destination
// ports, for callers (the spoof tester) that send from an ephemeral source port
// to a fixed listen port.
func buildUDPShimPorts(srcPort, dstPort uint16, src, dst net.IP, framed []byte) []byte {
	length := 8 + len(framed)
	b := make([]byte, length)
	binary.BigEndian.PutUint16(b[0:2], srcPort)
	binary.BigEndian.PutUint16(b[2:4], dstPort)
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
	pseudo := make([]byte, 12)
	copy(pseudo[0:4], src.To4())
	copy(pseudo[4:8], dst.To4())
	pseudo[9] = byte(proto)
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(segment)))
	return onesComplement(pseudo, segment)
}

// onesComplement is the internet checksum over the concatenation of the given
// byte slices: the ones-complement of the ones-complement 16-bit sum. It backs
// both the L4 (with a pseudo-header) and ICMP (without one) checksums.
func onesComplement(parts ...[]byte) uint16 {
	var sum uint32
	for _, b := range parts {
		for i := 0; i+1 < len(b); i += 2 {
			sum += uint32(b[i])<<8 | uint32(b[i+1])
		}
		if len(b)%2 == 1 {
			sum += uint32(b[len(b)-1]) << 8
		}
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// The ICMP echo profile carries the framed payload inside a ping. Its "shim" is
// the 8-byte ICMP echo header; unlike UDP/TCP its checksum spans only the ICMP
// message, with no IP pseudo-header. The type is what tells a client's send from
// a server's — echo request (8) vs echo reply (0) — the same split xdi uses so a
// pair looks like a ping and its answer on the wire.

const (
	icmpTypeEchoReply   = 0
	icmpTypeEchoRequest = 8
	icmpEchoHeaderLen   = 8
)

// buildICMPEcho wraps framed in an ICMP echo header. typ is the echo type this
// side sends (request from the client, reply from the server); id and seq fill
// the echo identifier and sequence fields.
func buildICMPEcho(typ byte, id, seq uint16, framed []byte) []byte {
	b := make([]byte, icmpEchoHeaderLen+len(framed))
	b[0] = typ
	b[1] = 0 // code
	// b[2:4] checksum, left zero for the computation
	binary.BigEndian.PutUint16(b[4:6], id)
	binary.BigEndian.PutUint16(b[6:8], seq)
	copy(b[icmpEchoHeaderLen:], framed)
	binary.BigEndian.PutUint16(b[2:4], onesComplement(b))
	return b
}

// parseICMPEcho validates an incoming ICMP message as an echo carrying this
// tunnel's identifier and returns the framed payload inside. Both echo types are
// accepted; which direction is this tunnel's is decided afterwards by the frame's
// direction byte, exactly as xdi does — that is what discards the kernel's
// automatic echo reply to our own request.
func parseICMPEcho(id uint16, msg []byte) (framed []byte, ok bool) {
	if len(msg) < icmpEchoHeaderLen {
		return nil, false
	}
	if msg[0] != icmpTypeEchoRequest && msg[0] != icmpTypeEchoReply {
		return nil, false
	}
	if binary.BigEndian.Uint16(msg[4:6]) != id {
		return nil, false
	}
	return msg[icmpEchoHeaderLen:], true
}
