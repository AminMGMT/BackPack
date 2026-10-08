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
	frame := GetDatagramBuffer(2 + len(payload))
	defer PutDatagramBuffer(frame)
	binary.BigEndian.PutUint16(frame[:2], uint16(len(payload)))
	copy(frame[2:], payload)
	for len(frame) > 0 {
		n, err := w.Write(frame)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

// ReadDatagram reads one length-prefixed datagram into buf and returns its
// size. buf must be at least MaxDatagram bytes, or a legal datagram that does
// not fit would have to be discarded mid-frame, which desynchronises the
// stream — every following datagram would be read from the wrong offset.
func ReadDatagram(r io.Reader, buf []byte) (int, error) {
	var hdr []byte
	if len(buf) >= 2 {
		hdr = buf[:2]
	} else {
		hdr = make([]byte, 2)
	}
	if _, err := io.ReadFull(r, hdr); err != nil {
		return 0, err
	}
	size := int(binary.BigEndian.Uint16(hdr))
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

// ReadDatagramInto reads one complete frame, growing a reusable receive buffer
// to its payload. The returned slice is valid until the next read into it.
// Small flows keep 2 KiB instead of reserving the maximum datagram size; growth
// is bounded by the 16-bit wire length, and a warmed flow does not allocate.
// On an incomplete frame it returns an empty slice and the read error.
func ReadDatagramInto(r io.Reader, buf []byte) ([]byte, error) {
	if cap(buf) < 2 {
		buf = make([]byte, 2048)
	}
	if _, err := io.ReadFull(r, buf[:2]); err != nil {
		return buf[:0], err
	}
	size := int(binary.BigEndian.Uint16(buf[:2]))
	if cap(buf) < size {
		capacity := MaxDatagram
		if size <= 2048 {
			capacity = 2048
		} else if size <= 16384 {
			capacity = 16384
		}
		buf = make([]byte, size, capacity)
	}
	buf = buf[:size]
	if _, err := io.ReadFull(r, buf); err != nil {
		return buf[:0], err
	}
	return buf, nil
}

var datagramClasses = [...]int{512, 2048, 16384, MaxDatagram + 2}
var datagramBuffers = [...]chan []byte{make(chan []byte, 64), make(chan []byte, 64), make(chan []byte, 32), make(chan []byte, 16)}

// GetDatagramBuffer lends a frame buffer. Put it back only after all users finish.
func GetDatagramBuffer(size int) []byte {
	for i, capacity := range datagramClasses {
		if size <= capacity {
			select {
			case b := <-datagramBuffers[i]:
				return b[:size]
			default:
				return make([]byte, size, capacity)
			}
		}
	}
	return make([]byte, size)
}

// PutDatagramBuffer retains a bounded number of reusable frames in each size class.
func PutDatagramBuffer(b []byte) {
	for i, capacity := range datagramClasses {
		if cap(b) == capacity {
			select {
			case datagramBuffers[i] <- b[:0]:
			default:
			}
			return
		}
	}
}
