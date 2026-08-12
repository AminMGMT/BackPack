package network

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// Framing and identity for the IP-spoofing carrier.
//
// The carrier used to prefix every payload with a session tag and a direction
// byte borrowed from xdi. It no longer does: to match the reference spoof
// transports, the KCP datagram now rides bare inside the profile's L4 header,
// and a packet is recognised as this tunnel's by the field the kernel already
// filters on — the UDP/TCP port, or the ICMP echo identifier — with the
// encryption above rejecting anything that shares that field by chance. The
// only thing derived from the token here now is that identifier; the tag half
// of spoofIdentity is dead weight kept only so the derivation stays stable.
//
// None of this was a security boundary — KCP's token-keyed cipher authenticates
// the tunnel, exactly as on every other carrier. The tag was a demultiplexer,
// and the port/identifier demultiplexes just as well without adding bytes to
// the wire.

// SpoofProfile is the L4 shim the carrier wraps around each datagram. It
// changes only what the packet looks like to inspection; everything above the
// L4 header is identical across profiles.
type SpoofProfile string

const (
	// SpoofProfileUDP wraps the payload in a UDP header. The default: the host
	// kernel has no automatic answer to an unknown UDP datagram, so nothing on
	// the machine fights the forged flow.
	SpoofProfileUDP SpoofProfile = "udp"
	// SpoofProfileTCP wraps the payload in a TCP header, which is what the
	// reference tools send. The host kernel WILL answer a forged TCP segment to
	// a port it is not listening on with a RST, which tears the flow down — so
	// this profile installs a targeted iptables rule that drops the kernel's
	// outbound RSTs for the tunnel's port. Needs the iptables binary.
	SpoofProfileTCP SpoofProfile = "tcp"
	// SpoofProfileICMP wraps the payload in an ICMP Echo Request, so on the wire
	// the tunnel looks like ping traffic, with a forged source. Both ends send
	// Echo Requests; the receiver keeps only those, so the kernel's automatic
	// Echo Reply is ignored and no firewall rule is needed.
	SpoofProfileICMP SpoofProfile = "icmp"
)

// ParseSpoofProfile validates a profile string, defaulting an empty one to UDP.
func ParseSpoofProfile(s string) (SpoofProfile, error) {
	switch SpoofProfile(s) {
	case "", SpoofProfileUDP:
		return SpoofProfileUDP, nil
	case SpoofProfileTCP:
		return SpoofProfileTCP, nil
	case SpoofProfileICMP:
		return SpoofProfileICMP, nil
	default:
		return "", fmt.Errorf("unknown spoof profile %q (supported: udp, tcp, icmp)", s)
	}
}

// ResolveSpoofDirections turns the profile knobs into an uplink/downlink pair:
// each direction is its own setting if given, otherwise the symmetric profile,
// otherwise udp. Validation happens at load time (checkSpoof), so a parse error
// here falls back to udp rather than failing.
func ResolveSpoofDirections(profile, uplink, downlink string) (SpoofProfile, SpoofProfile) {
	pick := func(dir string) SpoofProfile {
		v := dir
		if v == "" {
			v = profile
		}
		p, err := ParseSpoofProfile(v)
		if err != nil {
			return SpoofProfileUDP
		}
		return p
	}
	return pick(uplink), pick(downlink)
}

// ipProtocol is the IANA protocol number written into the IP header for a
// profile, and the one the receive socket binds to.
func (p SpoofProfile) ipProtocol() int {
	switch p {
	case SpoofProfileTCP:
		return 6 // IPPROTO_TCP
	case SpoofProfileICMP:
		return 1 // IPPROTO_ICMP
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
