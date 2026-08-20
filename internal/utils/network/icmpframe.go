package network

import (
	"crypto/sha256"
	"encoding/binary"
	"sync"
)

// The framing that carries the tunnel inside ICMP echo, and keeps two tunnels
// on one host from ever touching each other's traffic.
//
// ICMP has no ports. A raw ICMP socket receives every echo packet the host
// sees — so two tunnels opened on the same machine both read the same stream of
// packets, and something other than an address has to tell one tunnel's traffic
// from the other's, from a stray ping, and from the kernel's own automatic
// replies. That something is derived from the token, which the two ends of a
// tunnel already share and no other tunnel has:
//
//   - A 4-byte session tag prefixes every packet's payload. A packet whose tag
//     is not this tunnel's is not this tunnel's packet, full stop — a different
//     tunnel's, a bare ping's, anyone's. It is dropped without a further look.
//   - The 16-bit ICMP identifier is derived from the same token, so the field
//     the protocol already has for telling echo streams apart is used for
//     exactly that, and most tunnels differ in it before the tag is even read.
//   - A single direction byte says which way the packet is going. This is what
//     defeats the kernel: when a client's echo request reaches the server, the
//     server's kernel answers it automatically, echoing the request's payload —
//     tag and all — straight back to the client. That reply is indistinguishable
//     from a real one by tag or identifier alone, because it carries the
//     client's own. The direction byte is not the client's: the kernel copied
//     the client's outbound marker, and the client only accepts the server's.
//     So the kernel's helpfulness is discarded and no sysctl has to be touched.
//
// None of this is a security boundary — the token still authenticates the
// tunnel through KCP's own encrypted handshake, exactly as the UDP path does.
// It is a demultiplexer, and its whole job is that the wrong packet is never
// mistaken for the right one.

const (
	// xdiTagLen is the session tag width, and xdiHeaderLen the whole prefix the
	// payload carries: the tag plus one direction byte.
	xdiTagLen    = 4
	xdiHeaderLen = xdiTagLen + 1

	// The direction markers. Each end stamps its own on what it sends and
	// accepts only the other's on what it reads.
	xdiDirServer byte = 'S' // server -> client
	xdiDirClient byte = 'C' // client -> server
)

// xdiIdentity derives the ICMP identifier and session tag a tunnel uses, from
// its token. It is deterministic, so the two ends agree without exchanging
// anything, and distinct tokens almost never collide in both at once.
func xdiIdentity(token string) (id int, tag [xdiTagLen]byte) {
	sum := sha256.Sum256([]byte("backpack-xdi-v1:" + token))
	copy(tag[:], sum[:xdiTagLen])
	// The identifier is masked to 16 bits, which is all the ICMP field holds.
	id = int(binary.BigEndian.Uint16(sum[xdiTagLen : xdiTagLen+2]))
	return id, tag
}

// outboundDir is the direction marker a given side stamps on what it sends.
func outboundDir(server bool) byte {
	if server {
		return xdiDirServer
	}
	return xdiDirClient
}

// inboundDir is the direction marker a given side requires on what it accepts —
// the opposite of what it sends, which is what rejects the kernel's echoed-back
// replies.
func inboundDir(server bool) byte {
	if server {
		return xdiDirClient
	}
	return xdiDirServer
}

// encodeXdiPayload prefixes a KCP packet with the tag and direction that will
// let the other end recognise it and everything else reject it. The buffer is
// freshly sized so the caller may hold onto it.
func encodeXdiPayload(tag [xdiTagLen]byte, dir byte, payload []byte) []byte {
	out := make([]byte, xdiHeaderLen+len(payload))
	copy(out[:xdiTagLen], tag[:])
	out[xdiTagLen] = dir
	copy(out[xdiHeaderLen:], payload)
	return out
}

// appendXdiPayload is encodeXdiPayload without the allocation: it writes into a
// buffer the caller owns and reuses.
//
// The allocating form is kept for the places that genuinely want a fresh slice.
// The send path is not one of them — it runs once per packet, and a few
// thousand packets a second of short-lived slices is the garbage collector
// doing work the network asked nobody to do.
func appendXdiPayload(dst []byte, tag [xdiTagLen]byte, dir byte, payload []byte) []byte {
	dst = append(dst[:0], tag[:]...)
	dst = append(dst, dir)
	return append(dst, payload...)
}

// xdiBuffers backs both directions of the ICMP carrier. Sized for the largest
// echo this will ever build or read.
var xdiBuffers = sync.Pool{
	New: func() any {
		b := make([]byte, 65536)
		return &b
	},
}

// decodeXdiPayload checks an incoming ICMP echo payload against this tunnel's
// tag and the direction it is willing to accept, and returns the KCP packet
// inside when both match.
//
// ok is false for anything that is not this tunnel's inbound traffic — a
// different tunnel's tag, a bare ping too short to carry a header, the kernel's
// echoed-back copy of our own outbound packet. The caller drops those and reads
// the next.
func decodeXdiPayload(tag [xdiTagLen]byte, wantDir byte, data []byte) (payload []byte, ok bool) {
	if len(data) < xdiHeaderLen {
		return nil, false
	}
	for i := 0; i < xdiTagLen; i++ {
		if data[i] != tag[i] {
			return nil, false
		}
	}
	if data[xdiTagLen] != wantDir {
		return nil, false
	}
	return data[xdiHeaderLen:], true
}
