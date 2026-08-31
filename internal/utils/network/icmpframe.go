package network

import (
	"crypto/rand"
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
//   - The 16-bit ICMP identifier names one SESSION within the tunnel. A tunnel
//     is a control channel plus a pool of data connections, and each of those
//     is its own KCP session over its own socket — so the carrier has to be
//     able to tell them apart, and ICMP has no ports to do it with.
//
//     This is what the identifier is for in the protocol, and getting it wrong
//     stopped xdi working at all. It used to be derived from the token, which
//     made every session of a tunnel identical on the wire; kcp-go's listener
//     keys its sessions on the address the carrier reports, so all of them
//     collapsed onto one entry, and each new session closed the one before it
//     (see sess.go, "conversation id mismatched"). The control channel came up,
//     the first pool connection killed it, the reconnect killed that, and the
//     tunnel flapped forever while reporting itself connected.
//
//     So the client draws a random identifier per socket and accepts only its
//     own back; the server accepts every identifier — the tag and the direction
//     are what say the packet is this tunnel's — and reports the peer as
//     ip:identifier, which gives the listener the distinct addresses it needs.
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

// xdiTag derives the session tag a tunnel uses, from its token. It is
// deterministic, so the two ends agree without exchanging anything, and two
// tunnels on one host practically never collide.
func xdiTag(token string) (tag [xdiTagLen]byte) {
	sum := sha256.Sum256([]byte("backpack-xdi-v1:" + token))
	copy(tag[:], sum[:xdiTagLen])
	return tag
}

// The ICMP echo identifiers in use by this process, and the allocator that
// hands them out.
//
// Random alone would be enough against another host — those are separated by
// address before the identifier is looked at — but not against this one: the
// sessions that must never share an identifier are the several this client
// opens to the same server, and they are all in here. So they are drawn from a
// set rather than independently, and a collision is impossible instead of
// merely unlikely.
var xdiSessions = struct {
	mu   sync.Mutex
	used map[uint16]struct{}
}{used: map[uint16]struct{}{}}

// acquireXdiSessionID returns an identifier no other session in this process is
// using. It is released by the carrier's Close.
//
// Zero is skipped: it is what an identifier field reads as when it was never
// set, and a session that answered to it would answer to a malformed packet as
// readily as to its own.
func acquireXdiSessionID() uint16 {
	xdiSessions.mu.Lock()
	defer xdiSessions.mu.Unlock()
	var b [2]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			// A failed read from the system source is not a reason to refuse to
			// open a tunnel. Walk the space instead: the set below still
			// guarantees what actually matters, which is that no two sessions
			// here share an identifier.
			for id := uint16(1); id != 0; id++ {
				if _, taken := xdiSessions.used[id]; !taken {
					xdiSessions.used[id] = struct{}{}
					return id
				}
			}
			return 1 // 65535 sessions in one process; there is nothing left to give
		}
		id := binary.BigEndian.Uint16(b[:])
		if id == 0 {
			continue
		}
		if _, taken := xdiSessions.used[id]; taken {
			continue
		}
		xdiSessions.used[id] = struct{}{}
		return id
	}
}

// releaseXdiSessionID returns an identifier to the pool.
func releaseXdiSessionID(id uint16) {
	xdiSessions.mu.Lock()
	defer xdiSessions.mu.Unlock()
	delete(xdiSessions.used, id)
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
