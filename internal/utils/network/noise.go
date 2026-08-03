package network

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"sync"
	"time"

	"github.com/flynn/noise"
	"golang.org/x/crypto/hkdf"
)

// The stealth transport.
//
// Every other transport announces itself. TLS opens with a ClientHello whose
// fingerprint a censor can recognise and block; a raw or KCP stream has its own
// tells. This one has none: the handshake is a Noise NNpsk0 exchange, so on the
// wire it is two short bursts of bytes indistinguishable from random, and the
// stream that follows is a ChaCha20-Poly1305 record layer that looks the same.
// There is nothing for deep packet inspection to match against.
//
// The pre-shared key is derived from the tunnel token, which both ends already
// hold, so the transport needs no key of its own. Because that key is mixed in
// from the very first message, a peer without the token cannot produce a message
// the responder will accept — it is dropped and nothing is sent back, so a probe
// or a port scan finds a dead port rather than a service to fingerprint.
//
// NNpsk0 gives an encrypted, mutually authenticated, forward-secret channel
// without static keys to distribute. It authenticates the peer as "holds the
// token", which is exactly the tunnel's own trust model; the token check in the
// channel handshake still runs on top, unchanged.

const (
	// noisePrologue binds the handshake transcript to this application. It never
	// travels on the wire — both ends mix it in and must agree — so a handshake
	// captured from somewhere else cannot be replayed into this one.
	noisePrologue = "backpack-noise-v1"

	// noiseMaxPayload is the most plaintext a single Noise message can carry: the
	// 65535-byte message ceiling less the 16-byte authentication tag.
	noiseMaxPayload = 65535 - 16

	// noiseHandshakeTimeout bounds the handshake so a silent or stalling peer
	// cannot hold a half-open connection forever.
	noiseHandshakeTimeout = 15 * time.Second
)

// Hiding the shape of the traffic, not just its content.
//
// Encryption settles what is in a record; it says nothing about how big the
// record is, and record sizes are one of the few things left for an observer to
// work with on a stream they cannot read. Without padding every record is
// exactly as long as whatever the layer above handed down, so the wire carries
// a faithful transcript of the tunnel's own framing: a one-byte heartbeat, a
// token exchange of a known length, a burst of full-sized records for a
// download and small ones for an interactive session. None of that is secret in
// itself, but together it is a pattern, and a pattern is what a classifier
// needs.
//
// So each record carries a random amount of filler. Two decisions about where:
//
// The padding goes *inside* the encryption, unlike the scheme this was taken
// from, which frames the padding length in the clear and appends visible
// garbage. Padding an observer can find and subtract is not padding. Here the
// length byte and the filler are both under the AEAD, so all that reaches the
// wire is a record whose length moved.
//
// And the filler itself is left as zeroes. It is encrypted before it is sent,
// so its content is already indistinguishable from anything else; generating
// random bytes for it would cost a draw per record and buy nothing. Only the
// *length* has to be unpredictable, and that is what is drawn.

const (
	// noiseVersion is what this build advertises in its handshake payload.
	// Version 1 is the first that can pad. A peer that predates the negotiation
	// sends no payload at all, which reads as version 0 and leaves padding off,
	// so old and new ends still talk to each other.
	noiseVersion byte = 1

	// noiseVersionPadding is the version from which both ends pad.
	noiseVersionPadding byte = 1

	// noiseMaxPad is the most filler a record can carry. A byte's worth: enough
	// that a record's length says little about its payload, small enough that
	// the average cost — 128 bytes on a record that may be 64 KB — is noise.
	noiseMaxPad = 255

	// noisePaddedMaxPayload is the most plaintext a padded record can carry,
	// once the length byte and the largest possible filler are accounted for.
	noisePaddedMaxPayload = noiseMaxPayload - 1 - noiseMaxPad
)

// noiseSuite is fixed rather than negotiated: X25519, ChaCha20-Poly1305, BLAKE2s.
// One good choice both ends assume needs no negotiation on the wire, and a
// negotiation is one more thing that could be fingerprinted.
var noiseSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)

// noisePSK turns the tunnel token into the 32-byte pre-shared key.
func noisePSK(token string) ([]byte, error) {
	psk := make([]byte, 32)
	r := hkdf.New(sha256.New, []byte(token), []byte("backpack-stealth-psk-v1"), nil)
	if _, err := io.ReadFull(r, psk); err != nil {
		return nil, err
	}
	return psk, nil
}

// NoiseClientConn performs the initiator side of the handshake over raw and, on
// success, returns a net.Conn that transparently encrypts everything written to
// it and decrypts everything read from it.
func NoiseClientConn(raw net.Conn, token string, timeout time.Duration) (net.Conn, error) {
	return noiseHandshake(raw, token, true, timeout)
}

// NoiseServerConn is the responder side of NoiseClientConn.
func NoiseServerConn(raw net.Conn, token string, timeout time.Duration) (net.Conn, error) {
	return noiseHandshake(raw, token, false, timeout)
}

func noiseHandshake(raw net.Conn, token string, initiator bool, timeout time.Duration) (net.Conn, error) {
	psk, err := noisePSK(token)
	if err != nil {
		return nil, err
	}
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:           noiseSuite,
		Pattern:               noise.HandshakeNN,
		Initiator:             initiator,
		Prologue:              []byte(noisePrologue),
		PresharedKey:          psk,
		PresharedKeyPlacement: 0,
	})
	if err != nil {
		return nil, err
	}

	if timeout <= 0 {
		timeout = noiseHandshakeTimeout
	}
	// A deadline for the whole handshake; cleared once it completes so the
	// connection's normal deadlines apply from then on.
	_ = raw.SetDeadline(time.Now().Add(timeout))

	// The version each end advertises rides in the handshake payload, which is
	// already encrypted and already there — so agreeing on it costs no extra
	// round trip and adds nothing to the wire an observer can read.
	var sendCS, recvCS *noise.CipherState
	var peerVersion byte

	if initiator {
		// -> e
		msg, _, _, err := hs.WriteMessage(nil, []byte{noiseVersion})
		if err != nil {
			return nil, fmt.Errorf("noise: build first message: %w", err)
		}
		if err := writeNoiseFrame(raw, msg); err != nil {
			return nil, err
		}
		// <- e, ee
		in, err := readNoiseFrame(raw)
		if err != nil {
			return nil, err
		}
		// A wrong token (or anything that is not the peer) fails here.
		payload, cs0, cs1, err := hs.ReadMessage(nil, in)
		if err != nil {
			return nil, fmt.Errorf("noise: handshake rejected: %w", err)
		}
		peerVersion = versionFromPayload(payload)
		sendCS, recvCS = cs0, cs1 // initiator sends with cs0, receives with cs1
	} else {
		// -> e
		in, err := readNoiseFrame(raw)
		if err != nil {
			return nil, err
		}
		payload, _, _, err := hs.ReadMessage(nil, in)
		if err != nil {
			return nil, fmt.Errorf("noise: handshake rejected: %w", err)
		}
		peerVersion = versionFromPayload(payload)

		// Answer in kind. A peer that sent no version cannot read one, and
		// against an initiator that predates this the reply must look exactly
		// as it always did.
		var reply []byte
		if peerVersion > 0 {
			reply = []byte{noiseVersion}
		}
		// <- e, ee
		msg, cs0, cs1, err := hs.WriteMessage(nil, reply)
		if err != nil {
			return nil, fmt.Errorf("noise: build reply: %w", err)
		}
		if err := writeNoiseFrame(raw, msg); err != nil {
			return nil, err
		}
		sendCS, recvCS = cs1, cs0 // responder sends with cs1, receives with cs0
	}

	_ = raw.SetDeadline(time.Time{})

	if sendCS == nil || recvCS == nil {
		return nil, fmt.Errorf("noise: handshake did not complete")
	}

	// Padding needs both ends: one side padding and the other not reading the
	// length byte would corrupt every record.
	pad := noiseVersion >= noiseVersionPadding && peerVersion >= noiseVersionPadding

	return &noiseConn{Conn: raw, send: sendCS, recv: recvCS, pad: pad}, nil
}

// versionFromPayload reads the version a peer advertised. An empty payload is
// a peer that predates the negotiation, which is version 0.
func versionFromPayload(payload []byte) byte {
	if len(payload) == 0 {
		return 0
	}
	return payload[0]
}

// noiseConn is an encrypted net.Conn: the record layer over a completed Noise
// handshake. Plaintext written to it is split into Noise messages, each length-
// prefixed and encrypted; ciphertext read from it is decrypted and buffered.
type noiseConn struct {
	net.Conn

	// pad reports whether both ends agreed to pad their records. Set once at
	// handshake time and never written again.
	pad bool

	writeMu sync.Mutex
	send    *noise.CipherState
	padBuf  []byte // reused plaintext buffer: length byte, payload, filler

	readMu  sync.Mutex
	recv    *noise.CipherState
	readBuf []byte // decrypted plaintext not yet handed to the caller
}

// Write encrypts p and sends it as one or more Noise records. It honours
// net.Conn semantics: on success every byte of p has been written.
func (c *noiseConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	limit := noiseMaxPayload
	if c.pad {
		limit = noisePaddedMaxPayload
	}

	total := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > limit {
			chunk = chunk[:limit]
		}
		enc, err := c.send.Encrypt(nil, nil, c.plaintext(chunk))
		if err != nil {
			return total, err
		}
		if err := writeNoiseFrame(c.Conn, enc); err != nil {
			return total, err
		}
		total += len(chunk)
		p = p[len(chunk):]
	}
	return total, nil
}

// plaintext returns what actually goes into a record: the chunk itself when
// this connection does not pad, and otherwise the chunk wrapped in a length
// byte and a random amount of filler.
//
// The caller holds writeMu, which is also what makes reusing the buffer safe.
func (c *noiseConn) plaintext(chunk []byte) []byte {
	if !c.pad {
		return chunk
	}

	padLen := rand.IntN(noiseMaxPad + 1)
	need := 1 + len(chunk) + padLen
	if cap(c.padBuf) < need {
		c.padBuf = make([]byte, need)
	}
	c.padBuf = c.padBuf[:need]

	c.padBuf[0] = byte(padLen)
	copy(c.padBuf[1:], chunk)
	// The filler must be zeroed rather than left as whatever the buffer held.
	// It is reused across records, so skipping this would send the tail of a
	// previous record's plaintext as this one's padding — encrypted, but sent
	// a second time under a different key stream, and to no purpose.
	clear(c.padBuf[1+len(chunk):])

	return c.padBuf
}

// unpad strips what plaintext added. It is strict about the lengths because a
// record that does not describe itself is either a bug or a peer that should
// not be trusted to keep going.
func (c *noiseConn) unpad(plain []byte) ([]byte, error) {
	if !c.pad {
		return plain, nil
	}
	if len(plain) < 1 {
		return nil, fmt.Errorf("noise: padded record is empty")
	}
	padLen := int(plain[0])
	if 1+padLen > len(plain) {
		return nil, fmt.Errorf("noise: padded record claims %d bytes of padding in %d bytes", padLen, len(plain))
	}
	return plain[1 : len(plain)-padLen], nil
}

// Read returns decrypted plaintext, reading and decrypting another record only
// when its buffer is empty.
func (c *noiseConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	// A loop rather than a single attempt: a padded record can legitimately
	// carry no payload at all, and returning (0, nil) from Read is the kind of
	// answer callers are entitled not to expect.
	for len(c.readBuf) == 0 {
		frame, err := readNoiseFrame(c.Conn)
		if err != nil {
			return 0, err
		}
		plain, err := c.recv.Decrypt(nil, nil, frame)
		if err != nil {
			// A record that does not authenticate is not a short read to paper
			// over — the stream's integrity is gone. Surface it.
			return 0, fmt.Errorf("noise: record failed authentication: %w", err)
		}
		if plain, err = c.unpad(plain); err != nil {
			return 0, err
		}
		c.readBuf = plain
	}

	n := copy(p, c.readBuf)
	c.readBuf = c.readBuf[n:]
	return n, nil
}

// writeNoiseFrame prefixes a message with its length and writes it whole.
func writeNoiseFrame(w io.Writer, msg []byte) error {
	if len(msg) > 65535 {
		return fmt.Errorf("noise: message too large (%d bytes)", len(msg))
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(msg)))
	if _, err := w.Write(append(hdr[:], msg...)); err != nil {
		return err
	}
	return nil
}

// readNoiseFrame reads one length-prefixed message.
func readNoiseFrame(r io.Reader) ([]byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint16(hdr[:])
	if n == 0 {
		return nil, fmt.Errorf("noise: empty frame")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
