package paqet

import "encoding/binary"

// Recognising the previous PCK.
//
// The raw-datagram PCK this engine replaces carried the layer-3 tunnel's own
// sealed datagrams as the TCP payload, opening with a 13-byte header in the
// clear: a kind byte, a session id and a 64-bit counter (internal/tunnel/l3,
// wire.go). A handshake, the first thing either end sends, is kind 1 (init) or
// 2 (response). Their counter field is zero or, from a dialler that announces
// its protocol version there, a small number (internal/tunnel/l3, version.go).
// KCP's datagrams are encrypted from the first
// byte, so they match that shape with probability about 2^-55; a match is the other
// end still running the old PCK and nothing else.
//
// Those segments are dropped here rather than handed to KCP, which would
// discard them without a word, and the wire reports them so the engine can say
// what is wrong: the old peer's handshake is the only thing an operator who
// has updated one server and not the other will ever see.
//
// internal/tunnel/l3 holds a test that builds real handshake datagrams and
// checks this function recognises them, so a change to that header cannot
// leave this blind.
func looksLikeOldPck(p []byte) bool {
	const headerLen = 13
	if len(p) < headerLen+16 { // a handshake carries at least a key
		return false
	}
	if p[0] != 1 && p[0] != 2 {
		return false
	}
	// "Small" is below 256: versions are single digits, and the bound keeps the
	// false-positive rate negligible.
	return binary.BigEndian.Uint64(p[5:13]) < 256
}

// LooksLikeOldPck exposes the check for the test that ties it to the l3 wire.
func LooksLikeOldPck(p []byte) bool { return looksLikeOldPck(p) }
