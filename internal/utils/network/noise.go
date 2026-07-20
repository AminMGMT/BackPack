package network

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
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

	var sendCS, recvCS *noise.CipherState
	if initiator {
		// -> e
		msg, _, _, err := hs.WriteMessage(nil, nil)
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
		_, cs0, cs1, err := hs.ReadMessage(nil, in)
		if err != nil {
			return nil, fmt.Errorf("noise: handshake rejected: %w", err)
		}
		sendCS, recvCS = cs0, cs1 // initiator sends with cs0, receives with cs1
	} else {
		// -> e
		in, err := readNoiseFrame(raw)
		if err != nil {
			return nil, err
		}
		if _, _, _, err := hs.ReadMessage(nil, in); err != nil {
			return nil, fmt.Errorf("noise: handshake rejected: %w", err)
		}
		// <- e, ee
		msg, cs0, cs1, err := hs.WriteMessage(nil, nil)
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
	return &noiseConn{Conn: raw, send: sendCS, recv: recvCS}, nil
}

// noiseConn is an encrypted net.Conn: the record layer over a completed Noise
// handshake. Plaintext written to it is split into Noise messages, each length-
// prefixed and encrypted; ciphertext read from it is decrypted and buffered.
type noiseConn struct {
	net.Conn

	writeMu sync.Mutex
	send    *noise.CipherState

	readMu  sync.Mutex
	recv    *noise.CipherState
	readBuf []byte // decrypted plaintext not yet handed to the caller
}

// Write encrypts p and sends it as one or more Noise records. It honours
// net.Conn semantics: on success every byte of p has been written.
func (c *noiseConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	total := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > noiseMaxPayload {
			chunk = chunk[:noiseMaxPayload]
		}
		enc, err := c.send.Encrypt(nil, nil, chunk)
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

// Read returns decrypted plaintext, reading and decrypting another record only
// when its buffer is empty.
func (c *noiseConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	if len(c.readBuf) == 0 {
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
