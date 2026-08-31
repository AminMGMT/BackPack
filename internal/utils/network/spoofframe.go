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
	// SpoofProfileICMPv6 carries the payload in an ICMPv6 Echo Request (type
	// 128) that rides inside an ORDINARY IPv4 packet whose protocol byte is 58
	// (ICMPv6's number). It is not real IPv6 — the outer packet is IPv4 with a
	// forged IPv4 source, exactly like the icmp profile — but many filters that
	// clamp down hard on ICMP and UDP leave protocol 58 comparatively open,
	// because IPv6 itself is cut off and its ICMP is treated as harmless. The
	// reference spoof-tunnel ships this same trick. The kernel does not process
	// proto-58 payloads in an IPv4 packet as ICMP, so unlike the icmp profile it
	// generates no automatic reply and needs no suppression.
	SpoofProfileICMPv6 SpoofProfile = "icmpv6"
	// SpoofProfileIPIP carries the payload directly as the body of an IP-in-IP
	// packet (protocol 4), with no L4 header at all. Some filters that inspect
	// UDP/TCP/ICMP wave IP-in-IP through, since it is normally a router-to-router
	// encapsulation. There is no port or identifier to demultiplex on, so a
	// receiver leans on spoof_peer_src_ip and the encryption above; pin the
	// former for a clean feed.
	SpoofProfileIPIP SpoofProfile = "ipip"
	// SpoofProfileProto58 carries the payload directly as the body of an IPv4
	// packet whose protocol byte is 58, with no L4 header at all. It is the
	// reference spoof-tunnel's "proto58", and it is the icmpv6 profile with the
	// echo header taken off: the same protocol number that filters tend to leave
	// open, without the eight bytes that make it look like a ping.
	//
	// Which to reach for is a question about the filter, not about efficiency.
	// A path that passes protocol 58 whatever is inside it takes this; one that
	// looks at the payload and expects an ICMPv6 message takes icmpv6. Like ipip
	// and gre it carries no port, so a receiver leans on spoof_peer_src_ip and
	// the encryption above — and two tunnels sharing this host on protocol 58,
	// including an icmpv6 one, are told apart by exactly that.
	SpoofProfileProto58 SpoofProfile = "proto58"
	// SpoofProfileGRE carries the payload after a minimal 4-byte GRE header
	// (protocol 47), the generic routing encapsulation firewalls see between
	// routers. Like ipip it has no port to demultiplex on; pin spoof_peer_src_ip.
	SpoofProfileGRE SpoofProfile = "gre"
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
	case SpoofProfileICMPv6:
		return SpoofProfileICMPv6, nil
	case SpoofProfileIPIP:
		return SpoofProfileIPIP, nil
	case SpoofProfileProto58:
		return SpoofProfileProto58, nil
	case SpoofProfileGRE:
		return SpoofProfileGRE, nil
	default:
		return "", fmt.Errorf("unknown spoof profile %q (supported: udp, tcp, icmp, icmpv6, ipip, gre)", s)
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
	case SpoofProfileICMPv6, SpoofProfileProto58:
		return 58 // IPPROTO_ICMPV6, carried inside an IPv4 packet
	case SpoofProfileIPIP:
		return 4 // IPPROTO_IPIP
	case SpoofProfileGRE:
		return 47 // IPPROTO_GRE
	default:
		return 17 // IPPROTO_UDP
	}
}

// hasPortDemux reports whether a profile carries a port or identifier the
// receiver can filter on in the kernel. ipip and gre do not — they have no L4
// header — so their receiver accepts every packet of the protocol and leans on
// the source-IP pin and the encryption above.
func (p SpoofProfile) hasPortDemux() bool {
	return p != SpoofProfileIPIP && p != SpoofProfileGRE && p != SpoofProfileProto58
}

// isICMPFamily reports whether a profile carries its payload in an echo message
// (ICMP or the ICMPv6-in-IPv4 variant), which share framing and differ only in
// their IP protocol number and echo type numbers.
func (p SpoofProfile) isICMPFamily() bool {
	return p == SpoofProfileICMP || p == SpoofProfileICMPv6
}

// icmpEchoTypes returns the echo "request" and "reply" type numbers for an
// ICMP-family profile: 8/0 for ICMP, 128/129 for ICMPv6.
func (p SpoofProfile) icmpEchoTypes() (request, reply byte) {
	if p == SpoofProfileICMPv6 {
		return icmpv6TypeEchoRequest, icmpv6TypeEchoReply
	}
	return icmpTypeEchoRequest, icmpTypeEchoReply
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
