package network

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net"

	"golang.org/x/net/ipv4"
)

// SpoofDPI gathers the optional obfuscation knobs the carrier can apply to make
// the forged flow harder to fingerprint, ported from the reference spooftunnel.
// Every one is off by default; those that change the wire (padding, fake TLS)
// must be set the same on both ends, while the header cosmetics (ttl, dscp,
// source-port shuffle) need no agreement because the receiver ignores those
// fields.
type SpoofDPI struct {
	TTLJitter   bool   // vary the IP TTL per packet across a pool of OS defaults
	RandomDSCP  bool   // vary the IP DSCP/ToS per packet across plausible values
	ShufflePort bool   // randomise the L4 SOURCE port per packet (udp/tcp)
	PortMin     uint16 // low end of the source-port shuffle range
	PortMax     uint16 // high end of the source-port shuffle range
	Padding     bool   // append self-describing random padding to each payload
	PaddingMax  uint8  // most padding bytes to add (1..255); 0 means a small default
	FakeTLS     bool   // prepend a fake TLS record header (tcp profile only)
}

// The pools the jitter picks from, matching spooftunnel: realistic initial TTLs
// seen from common operating systems, and a few plausible DSCP/ToS values.
var (
	ttlPool  = [...]byte{64, 128, 255}
	dscpPool = [...]byte{0x00, 0x28, 0x10}
)

// pickTTL returns the TTL to stamp: a fixed 64 unless jitter is on, then one of
// the pool at random. TTL is not checked by the receiver, so this needs no
// coordination.
func (d SpoofDPI) pickTTL() int {
	if d.TTLJitter {
		return int(ttlPool[rand.IntN(len(ttlPool))])
	}
	return 64
}

// pickDSCP returns the ToS byte: 0 unless random DSCP is on. Also unchecked by
// the receiver.
func (d SpoofDPI) pickDSCP() int {
	if d.RandomDSCP {
		return int(dscpPool[rand.IntN(len(dscpPool))])
	}
	return 0
}

// pickSrcPort returns the L4 source port for the next packet: the fixed tunnel
// port unless shuffle is on, then a random port in the configured range. The
// destination port stays fixed, so the receiver's demux is unaffected.
func (d SpoofDPI) pickSrcPort(fixed uint16) uint16 {
	if !d.ShufflePort {
		return fixed
	}
	lo, hi := d.PortMin, d.PortMax
	if lo == 0 {
		lo = 49152
	}
	if hi <= lo {
		hi = 65535
	}
	return lo + uint16(rand.IntN(int(hi-lo)+1))
}

// tlsFakeHeaderLen is the size of the fake TLS record header the fakeTLS knob
// prepends: content-type (application_data), version (TLS 1.2), and length.
const tlsFakeHeaderLen = 5

// applyFakeTLS prepends a fake TLS 1.2 application-data record header to p, so a
// middlebox reading the start of the segment sees a TLS record. Length is p's
// length, capped at 16 bits as a real record is.
func applyFakeTLS(p []byte) []byte {
	b := make([]byte, tlsFakeHeaderLen+len(p))
	b[0] = 0x17             // content type: application_data
	b[1], b[2] = 0x03, 0x03 // version: TLS 1.2
	binary.BigEndian.PutUint16(b[3:5], uint16(len(p)))
	copy(b[tlsFakeHeaderLen:], p)
	return b
}

// stripFakeTLS removes the fake TLS record header, validating the type and
// version so a stray segment is rejected rather than mis-sliced.
func stripFakeTLS(p []byte) ([]byte, bool) {
	if len(p) < tlsFakeHeaderLen || p[0] != 0x17 || p[1] != 0x03 || p[2] != 0x03 {
		return nil, false
	}
	return p[tlsFakeHeaderLen:], true
}

// applyPadding appends 1..max random bytes to p, the last of which is the pad
// length itself, so the receiver can strip it with no separate length field —
// the self-describing scheme spooftunnel uses. The pad is added before the L4
// shim, so the checksum and the encryption above both cover it.
func applyPadding(p []byte, max uint8) []byte {
	if max == 0 {
		max = 64
	}
	padLen := int(rand.IntN(int(max))) + 1 // 1..max
	b := make([]byte, len(p)+padLen)
	copy(b, p)
	for i := len(p); i < len(b)-1; i++ {
		b[i] = byte(rand.IntN(256))
	}
	b[len(b)-1] = byte(padLen)
	return b
}

// stripPadding removes self-describing padding: the last byte is its length.
// Returns ok=false if that length is impossible, so a corrupt or unpadded frame
// is dropped rather than truncated into garbage.
func stripPadding(p []byte) ([]byte, bool) {
	if len(p) == 0 {
		return nil, false
	}
	padLen := int(p[len(p)-1])
	if padLen == 0 || padLen > len(p) {
		return nil, false
	}
	return p[:len(p)-padLen], true
}

// spoofFragRange is one IP fragment's slice of an L4 segment.
type spoofFragRange struct {
	off, end int  // byte range within the segment
	more     bool // the MoreFragments flag, set on all but the last
}

// spoofFragments splits an L4 segment of segLen bytes into IP fragments each no
// larger than mtu (counting the 20-byte IPv4 header). Every fragment but the
// last carries a multiple of 8 payload bytes, as IP fragmentation requires, and
// their offsets tile the segment with no gap or overlap. A segment that already
// fits the MTU comes back as a single fragment with more=false. Pure and
// cross-platform so the offset arithmetic — the part a wrong value would corrupt
// silently — is unit tested without a socket.
func spoofFragments(segLen, mtu int) []spoofFragRange {
	if ipv4.HeaderLen+segLen <= mtu {
		return []spoofFragRange{{0, segLen, false}}
	}
	maxData := ((mtu - ipv4.HeaderLen) / 8) * 8
	if maxData <= 0 {
		maxData = 8
	}
	var out []spoofFragRange
	for off := 0; off < segLen; off += maxData {
		end := off + maxData
		if end > segLen {
			end = segLen
		}
		out = append(out, spoofFragRange{off, end, end < segLen})
	}
	return out
}

// defaultSpoofSockBuf is the send/receive socket-buffer size the carrier asks
// the kernel for when the config leaves it at 0. It matches the reference
// spoof-tunnel's 4 MiB: a forged-source flow is one-directional, so the receive
// side has no back-pressure to slow the sender, and a small kernel buffer drops
// packets the instant the read loop is scheduled off. A buffer this size is what
// separates a throttled tunnel from a saturated link.
//
// It lives here, in the cross-platform file, so both the Linux carrier and the
// platform-independent pipe helper can reach it without duplicating the value.
const defaultSpoofSockBuf = 4 << 20

// defaultSpoofMTU is the largest IP packet the carrier emits before it fragments
// in userspace. 1500 is the classic Ethernet MTU; a payload plus its IP and L4
// headers over that is split into fragments the peer's kernel reassembles.
const defaultSpoofMTU = 1500

// sockBuffered is implemented by both *net.IPConn and *net.UDPConn: the socket
// buffer knobs the carrier tunes for throughput.
type sockBuffered interface {
	SetReadBuffer(int) error
	SetWriteBuffer(int) error
}

// applySockBuf enlarges a socket's send and receive buffers. Best effort: the
// kernel silently clamps to net.core.{r,w}mem_max, and a refusal is not worth
// failing the tunnel over, so the error is dropped — the tunnel still runs, just
// with the kernel default. sockBuf <= 0 leaves the socket untouched.
func applySockBuf(pc net.PacketConn, sockBuf int) {
	if sockBuf <= 0 {
		return
	}
	if s, ok := pc.(sockBuffered); ok {
		_ = s.SetReadBuffer(sockBuf)
		_ = s.SetWriteBuffer(sockBuf)
	}
}

// spoofConnOpts is everything one carrier needs, gathered into a struct so the
// growing set of knobs does not turn every constructor into a wall of positional
// arguments. Built from a SpoofCarrier (see SpoofCarrier.spoofOpts). Defined here
// in the cross-platform file so the Linux carrier and the non-Linux stub share
// one signature.
type spoofConnOpts struct {
	token      string
	uplink     SpoofProfile // client->server profile
	downlink   SpoofProfile // server->client profile
	realPeer   net.IP       // peer's real IPv4 (routing destination)
	srcIP      string       // forged source, "" keeps the real one
	srcPool    []string     // forged sources to rotate through
	iface      string       // egress device to pin the raw socket to
	sockBuf    int          // SO_SNDBUF/SO_RCVBUF, 0 = default
	replySplit bool         // icmp/icmpv6: client sends request, server sends reply
	peerSrc    string       // expected forged source of inbound packets, "" = any
	mtu        int          // fragment sends larger than this, 0 = default 1500
	dpi        SpoofDPI     // optional obfuscation knobs
}

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
	case SpoofProfileIPIP:
		return 0 // no L4 header: the payload is the IP body
	case SpoofProfileGRE:
		return greHeaderLen
	default: // udp, icmp and icmpv6 all use an 8-byte header
		return 8
	}
}

// The minimal GRE header the gre profile prepends: 2 bytes of flags/version
// (all zero — no checksum, no key, version 0) and a 2-byte protocol type. 0x0800
// (IPv4) is the plausible inner type a router would carry.
const (
	greHeaderLen = 4
	greProtoIPv4 = 0x0800
)

// buildGREShim wraps framed in a 4-byte GRE header. GRE has no checksum of its
// own when the checksum-present flag is clear, so there is nothing to compute.
func buildGREShim(framed []byte) []byte {
	b := make([]byte, greHeaderLen+len(framed))
	// b[0:2] flags+version = 0
	binary.BigEndian.PutUint16(b[2:4], greProtoIPv4)
	copy(b[greHeaderLen:], framed)
	return b
}

// stripGREShim returns the payload after a GRE header, or ok=false if the packet
// is too short to hold one.
func stripGREShim(l4 []byte) ([]byte, bool) {
	if len(l4) < greHeaderLen {
		return nil, false
	}
	return l4[greHeaderLen:], true
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
	return buildTCPShimPorts(port, port, seq, src, dst, framed)
}

// buildTCPShimPorts is buildTCPShim with independent source and destination
// ports, for the source-port shuffle. The destination port stays the tunnel's,
// so the receiver still matches it; only the source varies.
func buildTCPShimPorts(srcPort, dstPort uint16, seq uint32, src, dst net.IP, framed []byte) []byte {
	length := 20 + len(framed)
	b := make([]byte, length)
	binary.BigEndian.PutUint16(b[0:2], srcPort) // source port
	binary.BigEndian.PutUint16(b[2:4], dstPort) // dest port
	binary.BigEndian.PutUint32(b[4:8], seq)     // seq
	binary.BigEndian.PutUint32(b[8:12], 0)      // ack
	b[12] = 5 << 4                              // data offset = 5 words, no options
	b[13] = 0x18                                // PSH | ACK, so it reads as established traffic
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
	// The ICMPv6 echo types, carried inside an IPv4 packet by the icmpv6
	// profile. Same 8-byte echo header as ICMP, only the type numbers differ.
	icmpv6TypeEchoRequest = 128
	icmpv6TypeEchoReply   = 129
	icmpEchoHeaderLen     = 8
)

// buildICMPEcho wraps payload in an ICMP echo header. Both ends send an Echo
// Request (type 8); id and seq fill the echo identifier and sequence fields.
func buildICMPEcho(typ byte, id, seq uint16, payload []byte) []byte {
	b := make([]byte, icmpEchoHeaderLen+len(payload))
	b[0] = typ
	b[1] = 0 // code
	// b[2:4] checksum, left zero for the computation
	binary.BigEndian.PutUint16(b[4:6], id)
	binary.BigEndian.PutUint16(b[6:8], seq)
	copy(b[icmpEchoHeaderLen:], payload)
	binary.BigEndian.PutUint16(b[2:4], onesComplement(b))
	return b
}

// parseICMPEcho validates an incoming ICMP (or ICMPv6-in-IPv4) message as an
// echo of the wanted type carrying this tunnel's identifier, and returns the
// payload inside.
//
// wantType is the echo type this side expects from its peer: with both ends
// sending requests (the default) it is the request type, so the kernel's own
// automatic Echo Reply is discarded by type alone; with the reply split on it
// is the reply type on the client and the request type on the server. The
// identifier is the demux: a stray ping with a different id is not this
// tunnel's, and one that happens to share the id is rejected by the encryption
// above when it fails to decrypt.
func parseICMPEcho(wantType byte, id uint16, msg []byte) (payload []byte, ok bool) {
	if len(msg) < icmpEchoHeaderLen {
		return nil, false
	}
	if msg[0] != wantType {
		return nil, false
	}
	if binary.BigEndian.Uint16(msg[4:6]) != id {
		return nil, false
	}
	return msg[icmpEchoHeaderLen:], true
}
