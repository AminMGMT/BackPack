package l3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Encapsulation.
//
// Two formats, both implemented here rather than delegated to the kernel — see
// the package doc for why that is not optional.
//
// ipip is the degenerate case: the payload IS the inner IP packet, with no
// header of its own. It costs nothing and there is nothing to get wrong. The
// receiver still has to know whether it is looking at IPv4 or IPv6, and it
// does, because the first nibble of any IP packet is its version. That is also
// why there is no separate "sit" or "6in4" mode here: one ipip tunnel carries
// both families already.
//
// gre is RFC 2784 with the RFC 2890 key extension. It costs four bytes, or
// eight with a key, and buys three things ipip cannot offer: an explicit
// protocol field, a key that lets more than one logical tunnel share a
// carrier, and interoperability with the format everyone already knows.
//
// What is deliberately absent: the GRE checksum and sequence fields. The AEAD
// below already authenticates every byte, and the replay window already
// rejects duplicates and stale packets, so both fields would be weaker
// restatements of guarantees the layer above provides.

var (
	errEmptyPacket   = errors.New("l3: empty packet")
	errBadIPVersion  = errors.New("l3: payload is not an IPv4 or IPv6 packet")
	errShortGRE      = errors.New("l3: frame shorter than its GRE header")
	errGREFlags      = errors.New("l3: unsupported GRE flags")
	errGREKeyMissing = errors.New("l3: GRE key expected but not present")
	errGREKeyWrong   = errors.New("l3: GRE key does not match")
	errGREProto      = errors.New("l3: unsupported GRE protocol type")
)

// Ethertypes, which is what GRE names its payload with.
const (
	etherIPv4 uint16 = 0x0800
	etherIPv6 uint16 = 0x86DD
)

// greKeyPresent is the K bit in the GRE flags word (RFC 2890).
const greKeyPresent uint16 = 0x2000

// Encap is a framing wrapped around an inner IP packet. Implementations are
// stateless and safe for concurrent use.
type Encap interface {
	// Name is the value that selects this encapsulation in a config.
	Name() string

	// Overhead is how many bytes Wrap adds. It feeds the MTU calculation, so
	// it must be exact rather than an upper bound.
	Overhead() int

	// Wrap appends the encapsulated form of pkt to dst and returns the
	// extended buffer, in the append idiom.
	Wrap(dst, pkt []byte) ([]byte, error)

	// Unwrap returns the inner IP packet within frame. The result aliases
	// frame rather than copying.
	Unwrap(frame []byte) ([]byte, error)
}

// NewEncap builds the encapsulation for an [l3] config. A GRE key of zero means
// the key field is omitted entirely, which is the RFC 2784 form.
//
// There is one encapsulation now, and it is GRE.
//
// Offering two was offering a way for the two ends to disagree, and the cost of
// that disagreement is out of all proportion to what the choice bought: ipip
// saved four bytes and, when only one end had it, produced a tunnel that came
// up, reported a peer, logged nothing and carried nothing. The handshake
// refuses a mismatch by name now, so the failure is loud rather than silent —
// but the better answer is for there to be nothing to mismatch. GRE is also the
// one that can carry a key, which is what separates two tunnels between the
// same pair of addresses.
//
// A config that still says "ipip" is read as GRE rather than refused: it is a
// file somebody already has, and the update has to be able to run it. Both ends
// must be updated together, which the handshake will say plainly if they are
// not.
func NewEncap(name string, greKey uint32) (Encap, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "ipip", "gre":
		return greEncap{key: greKey, keyed: greKey != 0}, nil
	default:
		return nil, fmt.Errorf("l3: unknown encapsulation %q (the only one is \"gre\")", name)
	}
}

// ipVersion reads the version nibble that opens every IP packet. It is the
// whole of what ipip needs to tell the two families apart.
func ipVersion(pkt []byte) (byte, error) {
	if len(pkt) == 0 {
		return 0, errEmptyPacket
	}
	switch v := pkt[0] >> 4; v {
	case 4, 6:
		return v, nil
	default:
		return 0, errBadIPVersion
	}
}

// greEncap is RFC 2784, optionally with the RFC 2890 key.
type greEncap struct {
	key   uint32
	keyed bool
}

func (greEncap) Name() string { return "gre" }

func (g greEncap) Overhead() int {
	if g.keyed {
		return 8
	}
	return 4
}

func (g greEncap) Wrap(dst, pkt []byte) ([]byte, error) {
	version, err := ipVersion(pkt)
	if err != nil {
		return nil, err
	}
	proto := etherIPv4
	if version == 6 {
		proto = etherIPv6
	}

	var flags uint16
	if g.keyed {
		flags |= greKeyPresent
	}

	var hdr [8]byte
	binary.BigEndian.PutUint16(hdr[0:2], flags)
	binary.BigEndian.PutUint16(hdr[2:4], proto)
	if g.keyed {
		binary.BigEndian.PutUint32(hdr[4:8], g.key)
	}

	dst = append(dst, hdr[:g.Overhead()]...)
	return append(dst, pkt...), nil
}

func (g greEncap) Unwrap(frame []byte) ([]byte, error) {
	if len(frame) < 4 {
		return nil, errShortGRE
	}
	flags := binary.BigEndian.Uint16(frame[0:2])
	proto := binary.BigEndian.Uint16(frame[2:4])

	// Only the key bit is understood. Anything else set means the peer framed
	// the packet in a way this build cannot read, and guessing at the header
	// length from a flag we do not implement would misparse the payload.
	if flags&^greKeyPresent != 0 {
		return nil, errGREFlags
	}
	switch proto {
	case etherIPv4, etherIPv6:
	default:
		return nil, errGREProto
	}

	offset := 4
	if flags&greKeyPresent != 0 {
		if len(frame) < 8 {
			return nil, errShortGRE
		}
		if !g.keyed {
			// A keyed peer against an unkeyed local config: the two are not
			// the same tunnel, and accepting it would mix them.
			return nil, errGREKeyWrong
		}
		if binary.BigEndian.Uint32(frame[4:8]) != g.key {
			return nil, errGREKeyWrong
		}
		offset = 8
	} else if g.keyed {
		return nil, errGREKeyMissing
	}

	inner := frame[offset:]
	version, err := ipVersion(inner)
	if err != nil {
		return nil, err
	}
	// The protocol field and the packet must agree; if they do not, one of
	// them is wrong and there is no safe way to choose which.
	if (version == 4) != (proto == etherIPv4) {
		return nil, errGREProto
	}
	return inner, nil
}
