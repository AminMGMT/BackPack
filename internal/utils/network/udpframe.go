package network

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// Forwarded UDP rides the tunnel as a stream of length-prefixed datagrams, and
// this file holds the two things both ends must agree on: how a datagram is
// framed, and how a forwarded port says "this flow is UDP".
//
// The framing is deliberately symmetric — two bytes of length, then the
// payload, in both directions. An earlier version carried a four-byte
// timestamp from the client to the server and nothing the other way, to guess
// at congestion by comparing that stamp against the receiver's clock. Two
// machines do not share a clock: a kharej server a second ahead of the Iran one
// made every packet look a second late, which flagged every flow as congested
// and tore it down. The measurement was never sound, so it is gone rather than
// corrected.

// MaxDatagram is the largest forwarded datagram. It is what a two-byte length
// header can describe, and more than IP itself will carry in one packet.
const MaxDatagram = 65535

// udpScheme marks a forwarded target as UDP.
//
// The mark lives in the target address rather than in a new header byte
// because the address is the one field every transport already sends — the
// mux, websocket and QUIC transports carry it as a bare length-prefixed string
// with no room for a flag. Putting it here means forwarded UDP needs no change
// to any transport's framing, and a peer too old to understand it fails on that
// one flow with a resolve error instead of misreading the stream.
const udpScheme = "udp://"

// MarkUDP labels a forwarded target as UDP.
func MarkUDP(addr string) string { return udpScheme + addr }

// SplitUDPTarget strips the UDP mark, reporting whether it was there.
func SplitUDPTarget(addr string) (target string, isUDP bool) {
	if rest, ok := strings.CutPrefix(addr, udpScheme); ok {
		return rest, true
	}
	return addr, false
}

// WriteDatagram writes one length-prefixed datagram.
//
// A zero-length datagram is a real thing on the wire — a keepalive, a probe —
// and is framed like any other, so the far end sends a zero-length datagram
// too rather than nothing at all.
func WriteDatagram(w io.Writer, payload []byte) error {
	if len(payload) > MaxDatagram {
		return fmt.Errorf("datagram of %d bytes is too large to forward", len(payload))
	}
	frame := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(payload)))
	copy(frame[2:], payload)
	_, err := w.Write(frame)
	return err
}

// ReadDatagram reads one length-prefixed datagram into buf and returns its
// size. buf must be at least MaxDatagram bytes, or a legal datagram that does
// not fit would have to be discarded mid-frame, which desynchronises the
// stream — every following datagram would be read from the wrong offset.
func ReadDatagram(r io.Reader, buf []byte) (int, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, err
	}
	size := int(binary.BigEndian.Uint16(hdr[:]))
	if size > len(buf) {
		return 0, fmt.Errorf("datagram of %d bytes does not fit in a %d byte buffer", size, len(buf))
	}
	if size == 0 {
		return 0, nil
	}
	if _, err := io.ReadFull(r, buf[:size]); err != nil {
		return 0, err
	}
	return size, nil
}
