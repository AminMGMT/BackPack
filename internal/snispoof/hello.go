package snispoof

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

// A TLS ClientHello built to be seen and thrown away.
//
// The technique is patterniha's, by way of the Rust implementation at
// github.com/therealaleph/sni-spoofing-rust (MIT). The idea: send a filtering
// box a ClientHello naming a domain it allows, on a connection that is really
// going somewhere else. The box reads the name, decides the flow is permitted,
// and stops looking. The server the packet is addressed to never sees it — the
// sequence number puts it behind the window, so its stack discards it as data
// it has already taken. See inject_linux.go for that half.
//
// This file is only the message. It is a fixed 517 bytes because that is the
// size the technique was measured with: a middlebox that reassembles is being
// asked to parse one segment, and a length that varies with the domain would
// make the flows distinguishable from each other, which is the opposite of the
// point. The three random blocks are random per connection for the same
// reason — two connections that sent byte-identical hellos would stand out.
//
// Nothing here is a security boundary. The message is designed to be read.

// helloSize is what every hello weighs, whatever domain is in it.
const helloSize = 517

// maxSNI is the longest domain that still leaves room for the padding that
// brings the message up to helloSize.
const maxSNI = 219

// The parts of the template that never change: everything before the client
// random, the block between the session id and the server-name extension, and
// the block between the server name and the key share. They are a capture of a
// real modern ClientHello, so what is sent matches what a browser sends.
const (
	helloPrefix = "1603010200010001fc0303"

	helloMiddle = "0024130213031301c02cc030c02bc02fcca9cca8c024c028c023c027009f009e006b006700ff0100018f0000"

	helloSuffix = "000b000403000102000a00160014001d0017001e0019001801000101010201030104002300000010000e000c02683208687474702f312e310016000000170000000d002a0028040305030603080708080809080a080b080408050806040105010601030303010302040205020602002b00050403040303002d00020101003300260024001d0020"
)

// BuildHello assembles the message for one domain.
//
// The three blocks that must differ per connection — the client random, the
// session id and the key share — are drawn fresh every time.
func BuildHello(sni string) ([]byte, error) {
	if sni == "" {
		return nil, errors.New("snispoof: no domain to name in the hello")
	}
	if len(sni) > maxSNI {
		return nil, fmt.Errorf("snispoof: %q is %d bytes; the longest that fits is %d",
			sni, len(sni), maxSNI)
	}
	prefix, err := hex.DecodeString(helloPrefix)
	if err != nil {
		return nil, err
	}
	middle, err := hex.DecodeString(helloMiddle)
	if err != nil {
		return nil, err
	}
	suffix, err := hex.DecodeString(helloSuffix)
	if err != nil {
		return nil, err
	}

	random, sessionID, keyShare := make([]byte, 32), make([]byte, 32), make([]byte, 32)
	for _, b := range [][]byte{random, sessionID, keyShare} {
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("snispoof: %w", err)
		}
	}

	out := make([]byte, 0, helloSize)
	out = append(out, prefix...)
	out = append(out, random...)
	out = append(out, 0x20) // session id length
	out = append(out, sessionID...)
	out = append(out, middle...)

	// The server_name extension, sized around the domain.
	name := []byte(sni)
	out = binary.BigEndian.AppendUint16(out, uint16(len(name)+5)) // extension
	out = binary.BigEndian.AppendUint16(out, uint16(len(name)+3)) // list
	out = append(out, 0x00)                                       // host_name
	out = binary.BigEndian.AppendUint16(out, uint16(len(name)))
	out = append(out, name...)

	out = append(out, suffix...)
	out = append(out, keyShare...)

	// Padding (extension 21) takes up whatever the domain did not, so every
	// hello is the same length however long the name is.
	out = append(out, 0x00, 0x15)
	pad := maxSNI - len(name)
	out = binary.BigEndian.AppendUint16(out, uint16(pad))
	out = append(out, make([]byte, pad)...)

	if len(out) != helloSize {
		return nil, fmt.Errorf("snispoof: built %d bytes, want %d", len(out), helloSize)
	}
	return out, nil
}

// SNIOf reads the domain back out of a hello this package built. It is what the
// tests assert with, and what a diagnostic can use to show what was actually
// sent rather than what was configured.
func SNIOf(hello []byte) (string, bool) {
	const lenAt = 125 // where the two-byte name length sits, by construction
	if len(hello) < helloSize {
		return "", false
	}
	n := int(binary.BigEndian.Uint16(hello[lenAt : lenAt+2]))
	if lenAt+2+n > len(hello) {
		return "", false
	}
	return string(hello[lenAt+2 : lenAt+2+n]), true
}

// DefaultDomain is what a hello names when the operator has not chosen one.
//
// A domain inside the country, on infrastructure a filter is very unlikely to
// be blocking, is the point: the name has to be one the path already lets
// through. It is a default and not a rule — which names still pass is a
// property of the route, and the operator is the one who can find out.
const DefaultDomain = "mci.ir"

// DomainOr returns the configured domain, or the default when none is set.
func DomainOr(domain string) string {
	if domain == "" {
		return DefaultDomain
	}
	return domain
}

// LooksLikeHello reports whether a payload is one of these messages.
//
// Three things have to hold: a TLS record header, a client_hello inside it, and
// exactly the length this package emits. See the note in the l3 carrier for why
// the shape is enough and why a marker in every datagram would be worse.
func LooksLikeHello(p []byte) bool {
	return len(p) == helloSize &&
		p[0] == 0x16 && // handshake record
		p[1] == 0x03 && p[2] == 0x01 && // TLS 1.0 record version, as a real hello sends
		p[5] == 0x01 // client_hello
}
