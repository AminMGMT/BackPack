package l3

import (
	"encoding/binary"
	"errors"
)

// The wire format.
//
// Every datagram this package puts on a carrier opens with the same 13-byte
// header, in the clear:
//
//	 0      1                    5                            13
//	+------+--------------------+----------------------------+--------------+
//	| kind | session (uint32BE) | counter (uint64BE)         | body         |
//	+------+--------------------+----------------------------+--------------+
//
// The header is short and fixed because all three fields have to be readable
// before anything can be decrypted. The kind says which of the three messages
// this is; the session says which set of keys it belongs to, which is what
// lets a rekey overlap with the traffic it is replacing; and the counter is
// the AEAD nonce, which must be explicit because datagrams arrive out of order
// and a receiver cannot infer it from position the way a stream can.
//
// On a data message the header is also the additional authenticated data, so
// none of the three fields can be altered in flight without the tag failing.
// They are readable by an observer, but an observer learns only that some
// 13-byte-prefixed traffic is flowing — the same thing the carrier's own
// framing already tells them.
const (
	// typeInit is the initiator's first handshake message. Counter is 0.
	typeInit byte = 1
	// typeResp is the responder's reply. Counter is 0.
	typeResp byte = 2
	// typeData is an encapsulated IP packet under the session's keys.
	typeData byte = 3

	// typeProbe measures how large a packet the path will actually carry. It
	// is padded so that its size on the wire is exactly that of a data message
	// carrying a full-sized inner packet — a probe smaller than that would
	// answer a question nobody asked. Sealed under the session like data, so
	// nobody who does not hold the token can push the tunnel's MTU around.
	typeProbe byte = 4

	// typeProbeAck is the answer, and is deliberately small: what it proves is
	// that the probe arrived, and the return path is measured by the peer's
	// own probes rather than by this.
	typeProbeAck byte = 5
)

// headerLen is the fixed prefix on every message.
const headerLen = 13

// tagLen is what ChaCha20-Poly1305 appends to a data message.
const tagLen = 16

// dataOverhead is what a data message costs on top of the encapsulated packet:
// the header, which travels in the clear, and the authentication tag. It is
// what mtu.go subtracts from the path.
const dataOverhead = headerLen + tagLen

var (
	errShortPacket = errors.New("l3: datagram shorter than the header")
	errBadKind     = errors.New("l3: unrecognised message kind")
)

type header struct {
	kind    byte
	session uint32
	counter uint64
}

// put writes h into the first headerLen bytes of dst, which must be at least
// that long.
func (h header) put(dst []byte) {
	_ = dst[headerLen-1] // bounds check once
	dst[0] = h.kind
	binary.BigEndian.PutUint32(dst[1:5], h.session)
	binary.BigEndian.PutUint64(dst[5:13], h.counter)
}

// bytes returns h as a standalone array, for use as AEAD additional data where
// aliasing the output buffer would be awkward to reason about.
func (h header) bytes() [headerLen]byte {
	var b [headerLen]byte
	h.put(b[:])
	return b
}

// parseHeader splits a received datagram into its header and body. The body
// aliases buf; callers must not retain it past the read buffer's next use.
func parseHeader(buf []byte) (header, []byte, error) {
	if len(buf) < headerLen {
		return header{}, nil, errShortPacket
	}
	h := header{
		kind:    buf[0],
		session: binary.BigEndian.Uint32(buf[1:5]),
		counter: binary.BigEndian.Uint64(buf[5:13]),
	}
	switch h.kind {
	case typeInit, typeResp, typeData, typeProbe, typeProbeAck:
	default:
		return header{}, nil, errBadKind
	}
	return h, buf[headerLen:], nil
}
