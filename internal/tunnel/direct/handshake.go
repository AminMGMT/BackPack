package direct

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Proving both ends hold the token.
//
// The tunnel's only credential is a shared token, and it must not travel on
// the wire: anything that watches one session would then be able to open its
// own. So neither end sends it. Each draws a nonce, and each proves it holds
// the token by returning an HMAC over both nonces.
//
//	edge   -> origin   nonceE
//	origin -> edge     nonceO, HMAC(token, "origin" || nonceE || nonceO)
//	edge   -> origin   HMAC(token, "edge"   || nonceE || nonceO)
//
// Three properties fall out of that. It is mutual — the edge learns the origin
// holds the token before it sends anything of its own, so a wrong address does
// not leak a proof to whoever answers. It cannot be replayed, because both
// ends contribute freshness. And the two directions use different labels, so a
// proof captured in one direction is not a valid proof in the other.
//
// Over the stealth transport this sits inside the Noise record layer, which
// has already authenticated the peer from the same token. Keeping the exchange
// there anyway costs one round trip on a connection that is opened once, and
// means every transport is authenticated the same way rather than some being
// authenticated by a property of the transport underneath.

const (
	// nonceLen is what each end contributes to the transcript.
	nonceLen = 32

	// proofLen is the width of an HMAC-SHA256.
	proofLen = sha256.Size

	// handshakeTimeout bounds the exchange, so a peer that connects and then
	// says nothing cannot hold a session slot open.
	handshakeTimeout = 15 * time.Second

	// protocolVersion is sent first and must match. It exists so that a future
	// change to this exchange fails with a clear message instead of a stalled
	// connection.
	protocolVersion byte = 1
)

var (
	errBadProof   = errors.New("direct: the peer does not hold the tunnel token")
	errBadVersion = errors.New("direct: the peer speaks a different protocol version")
)

// proof is the value each end returns to show it holds the token.
func proof(token, label string, nonceEdge, nonceOrigin []byte) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("backpack-direct-v1|"))
	mac.Write([]byte(label))
	mac.Write([]byte{'|'})
	mac.Write(nonceEdge)
	mac.Write(nonceOrigin)
	return mac.Sum(nil)
}

// edgeHandshake is the dialling side's half.
func edgeHandshake(conn net.Conn, token string) error {
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	defer conn.SetDeadline(time.Time{})

	nonceEdge := make([]byte, nonceLen)
	if _, err := rand.Read(nonceEdge); err != nil {
		return fmt.Errorf("direct: drawing a nonce: %w", err)
	}
	if _, err := conn.Write(append([]byte{protocolVersion}, nonceEdge...)); err != nil {
		return fmt.Errorf("direct: sending the handshake: %w", err)
	}

	reply := make([]byte, nonceLen+proofLen)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("direct: reading the handshake reply: %w", err)
	}
	nonceOrigin, given := reply[:nonceLen], reply[nonceLen:]

	// Checked before anything of ours is sent, so a wrong address or an
	// impostor never receives a proof it could use elsewhere.
	if !hmac.Equal(given, proof(token, "origin", nonceEdge, nonceOrigin)) {
		return errBadProof
	}
	if _, err := conn.Write(proof(token, "edge", nonceEdge, nonceOrigin)); err != nil {
		return fmt.Errorf("direct: sending the proof: %w", err)
	}
	return nil
}

// originHandshake is the listening side's half.
func originHandshake(conn net.Conn, token string) error {
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	defer conn.SetDeadline(time.Time{})

	opening := make([]byte, 1+nonceLen)
	if _, err := io.ReadFull(conn, opening); err != nil {
		return fmt.Errorf("direct: reading the handshake: %w", err)
	}
	if opening[0] != protocolVersion {
		return errBadVersion
	}
	nonceEdge := opening[1:]

	nonceOrigin := make([]byte, nonceLen)
	if _, err := rand.Read(nonceOrigin); err != nil {
		return fmt.Errorf("direct: drawing a nonce: %w", err)
	}
	reply := append(nonceOrigin, proof(token, "origin", nonceEdge, nonceOrigin)...)
	if _, err := conn.Write(reply); err != nil {
		return fmt.Errorf("direct: sending the handshake reply: %w", err)
	}

	given := make([]byte, proofLen)
	if _, err := io.ReadFull(conn, given); err != nil {
		return fmt.Errorf("direct: reading the proof: %w", err)
	}
	if !hmac.Equal(given, proof(token, "edge", nonceEdge, nonceOrigin)) {
		return errBadProof
	}
	return nil
}

// The per-stream exchange.
//
// A stream carries one user conversation. The edge opens it and says what kind
// it is and where the bytes should go; the origin reaches that address and
// says whether it worked. Only then do the two ends start copying, so a
// backend that is down is reported as a failure rather than as a stream that
// opens and closes a moment later.
//
//	edge   -> origin   kind, target
//	origin -> edge     ok | failed
//	                   ...then the conversation

const (
	// streamOK and streamFailed are the origin's answer.
	streamOK     byte = 1
	streamFailed byte = 2

	// kindTCP joins the stream to a TCP connection, and is the ordinary case:
	// the stream is a byte pipe and so is the backend, so the two are simply
	// copied into each other.
	kindTCP byte = 1

	// kindUDP joins it to a UDP socket. A stream has no message boundaries and
	// datagrams are nothing but boundaries, so each one is length-prefixed
	// while it is on the stream. See writeDatagram.
	kindUDP byte = 2

	// maxTargetLen bounds the address a stream may ask for, so a peer cannot
	// make the origin allocate on the strength of a length field alone.
	maxTargetLen = 512

	// maxDatagram is the largest UDP payload that can be carried, which is
	// also the most a 16-bit length can describe.
	maxDatagram = 65535
)

// writeRequest opens the conversation: what kind of stream this is, and the
// backend it should be joined to.
func writeRequest(w io.Writer, kind byte, target string) error {
	if len(target) == 0 || len(target) > maxTargetLen {
		return fmt.Errorf("direct: target address of %d bytes is out of range", len(target))
	}
	buf := make([]byte, 3+len(target))
	buf[0] = kind
	binary.BigEndian.PutUint16(buf[1:3], uint16(len(target)))
	copy(buf[3:], target)
	_, err := w.Write(buf)
	return err
}

// readRequest reads what writeRequest sent.
func readRequest(r io.Reader) (byte, string, error) {
	var head [3]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, "", err
	}
	kind := head[0]
	if kind != kindTCP && kind != kindUDP {
		return 0, "", fmt.Errorf("direct: unrecognised stream kind %d", kind)
	}
	n := binary.BigEndian.Uint16(head[1:3])
	if n == 0 || int(n) > maxTargetLen {
		return 0, "", fmt.Errorf("direct: target address of %d bytes is out of range", n)
	}
	target := make([]byte, n)
	if _, err := io.ReadFull(r, target); err != nil {
		return 0, "", err
	}
	return kind, string(target), nil
}

// Datagrams on a stream.
//
// A mux stream is a byte pipe: it preserves order and content but not where
// one write ended and the next began. A datagram is the opposite — its
// boundary is the whole of its meaning, and a UDP receiver that was handed two
// datagrams joined together would be handed something neither sender sent. So
// each one carries its length while it is on the stream, and the length is
// stripped again before it reaches a socket.

// writeDatagram frames one datagram onto a stream.
func writeDatagram(w io.Writer, payload []byte) error {
	if len(payload) > maxDatagram {
		return fmt.Errorf("direct: datagram of %d bytes is larger than %d", len(payload), maxDatagram)
	}
	buf := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(buf[:2], uint16(len(payload)))
	copy(buf[2:], payload)
	// One write, so two datagrams cannot interleave on the stream.
	_, err := w.Write(buf)
	return err
}

// readDatagram reads one framed datagram into buf, which must be able to hold
// the largest the peer might send.
func readDatagram(r io.Reader, buf []byte) (int, error) {
	var length [2]byte
	if _, err := io.ReadFull(r, length[:]); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint16(length[:]))
	if n > len(buf) {
		return 0, fmt.Errorf("direct: datagram of %d bytes does not fit a %d-byte buffer", n, len(buf))
	}
	if n == 0 {
		return 0, nil // an empty datagram is a real thing a socket can send
	}
	if _, err := io.ReadFull(r, buf[:n]); err != nil {
		return 0, err
	}
	return n, nil
}
