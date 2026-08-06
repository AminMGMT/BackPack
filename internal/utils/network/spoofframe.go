package network

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// Framing and identity for the IP-spoofing carrier.
//
// A raw IPv4 socket, like the raw ICMP one xdi uses, receives every packet of
// its protocol the host sees — there is no port to bind. So the same problem
// arises and the same answer solves it: a session tag and a direction byte,
// both derived from the token, prefix the tunnel's payload inside whatever L4
// shim the profile wraps around it. A packet that does not carry this tunnel's
// tag and the direction this end accepts is not this tunnel's packet and is
// dropped. This reuses the tag/direction encoding the xdi frame already
// defines (encodeXdiPayload/decodeXdiPayload); only the identity derivation and
// the profile vocabulary are specific to spoofing.
//
// None of this is a security boundary — KCP's own encrypted handshake still
// authenticates the tunnel, exactly as on every other carrier. It is a
// demultiplexer, so the wrong packet is never mistaken for the right one.

// SpoofProfile is the L4 shim the carrier wraps around each datagram. It
// changes only what the packet looks like to inspection; the tag/direction
// framing and everything above are identical across profiles.
type SpoofProfile string

const (
	// SpoofProfileUDP wraps the payload in a UDP header. The default: the host
	// kernel has no automatic answer to an unknown UDP datagram, so nothing on
	// the machine fights the forged flow.
	SpoofProfileUDP SpoofProfile = "udp"
	// SpoofProfileTCP wraps the payload in a TCP header, which is what the
	// reference tools send. The host kernel WILL answer a forged TCP segment to
	// a port it is not listening on with a RST, which tears the flow down — so
	// this profile requires an iptables rule on both ends that drops outbound
	// RSTs for the spoofed pair. See the transport docs for the exact rule.
	SpoofProfileTCP SpoofProfile = "tcp"
)

// ParseSpoofProfile validates a profile string, defaulting an empty one to UDP.
func ParseSpoofProfile(s string) (SpoofProfile, error) {
	switch SpoofProfile(s) {
	case "", SpoofProfileUDP:
		return SpoofProfileUDP, nil
	case SpoofProfileTCP:
		return SpoofProfileTCP, nil
	default:
		return "", fmt.Errorf("unknown spoof profile %q (supported: udp, tcp)", s)
	}
}

// ipProtocol is the IANA protocol number written into the IP header for a
// profile, and the one the receive socket binds to.
func (p SpoofProfile) ipProtocol() int {
	switch p {
	case SpoofProfileTCP:
		return 6 // IPPROTO_TCP
	default:
		return 17 // IPPROTO_UDP
	}
}

// spoofIdentity derives the session tag and the L4 port a tunnel uses, from its
// token. Deterministic, so the two ends agree without exchanging anything, and
// distinct tokens almost never collide. The port is the port field written into
// the UDP/TCP shim — cosmetic, but stable per tunnel so a stateful middlebox
// sees a consistent flow.
func spoofIdentity(token string) (tag [xdiTagLen]byte, port uint16) {
	sum := sha256.Sum256([]byte("backpack-spoof-v1:" + token))
	copy(tag[:], sum[:xdiTagLen])
	port = binary.BigEndian.Uint16(sum[xdiTagLen : xdiTagLen+2])
	// Keep the port out of the low well-known range so it reads as an ephemeral
	// flow rather than a service.
	if port < 1024 {
		port += 1024
	}
	return tag, port
}
