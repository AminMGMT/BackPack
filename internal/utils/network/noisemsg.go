package network

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/flynn/noise"
)

// The stealth record layer over a transport that carries whole messages — a
// websocket — rather than a byte stream.
//
// The handshake is the stream one, run over the messages: each handshake frame
// travels as one binary message. After it, every message is sealed whole: its
// plaintext is cut into Noise records (each length-prefixed, padded like the
// stream's), and the records of one message travel as one message. Message
// boundaries survive, which is what lets a CDN or proxy that understands
// websockets carry the tunnel without seeing what is in it.

// MessageTransport is a connection that carries whole messages.
type MessageTransport interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
}

// wsBinaryMessage is a websocket binary message (RFC 6455 opcode 2, which is
// also gorilla/websocket's BinaryMessage).
const wsBinaryMessage = 2

// NoiseBox seals and opens messages for one connection.
type NoiseBox struct {
	pad bool
	// shape and mask make it stealth+: shaped two-byte filler, and every
	// message's bytes under the mask's keystream (see noiseplus.go).
	shape *plusShape
	mask  *maskConn

	sendMu sync.Mutex
	send   *noise.CipherState
	plain  []byte // reused plaintext of one record

	recvMu sync.Mutex
	recv   *noise.CipherState
}

// NoiseMessageClient runs the initiator's handshake over m.
func NoiseMessageClient(m MessageTransport, token string, timeout time.Duration) (*NoiseBox, error) {
	return noiseMessageHandshake(m, token, true, timeout)
}

// NoiseMessageServer runs the responder's handshake over m.
func NoiseMessageServer(m MessageTransport, token string, timeout time.Duration) (*NoiseBox, error) {
	return noiseMessageHandshake(m, token, false, timeout)
}

func noiseMessageHandshake(m MessageTransport, token string, initiator bool, timeout time.Duration) (*NoiseBox, error) {
	c, err := noiseHandshake(&msgStream{m: m}, token, initiator, timeout)
	if err != nil {
		return nil, err
	}
	nc := c.(*noiseConn)
	return &NoiseBox{pad: nc.pad, send: nc.send, recv: nc.recv}, nil
}

// StealthPlusMessageClient runs stealth+'s initiator handshake over m: the
// handshake messages masked, as on a stream.
func StealthPlusMessageClient(m MessageTransport, token string, timeout time.Duration) (*NoiseBox, error) {
	return plusMessageHandshake(m, token, true, timeout)
}

// StealthPlusMessageServer is the responder's.
func StealthPlusMessageServer(m MessageTransport, token string, timeout time.Duration) (*NoiseBox, error) {
	return plusMessageHandshake(m, token, false, timeout)
}

func plusMessageHandshake(m MessageTransport, token string, initiator bool, timeout time.Duration) (*NoiseBox, error) {
	mask := newMaskConn(&msgStream{m: m}, token)
	c, err := noiseHandshakeWith(mask, token, initiator, timeout, noiseOpts{prologue: plusPrologue, hsPad: true})
	if err != nil {
		return nil, err
	}
	nc := c.(*noiseConn)
	return &NoiseBox{send: nc.send, recv: nc.recv, shape: newPlusShape(token), mask: mask}, nil
}

// Seal encrypts msg into one message's payload. Messages must be written in
// the order they were sealed: the caller holds its write lock across both.
func (b *NoiseBox) Seal(msg []byte) ([]byte, error) { return b.SealTo(nil, msg) }

// SealTo is Seal appending to dst[:0], so a caller that writes the payload
// before sealing the next can reuse one buffer.
func (b *NoiseBox) SealTo(dst, msg []byte) ([]byte, error) {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	limit := noiseMaxPayload
	switch {
	case b.shape != nil:
		limit = plusMaxPayload
	case b.pad:
		limit = noisePaddedMaxPayload
	}
	need := len(msg) + (len(msg)/limit+1)*(noiseLenPrefix+16+plusPadHeader+plusShapeMax+32)
	out := dst[:0]
	if cap(out) < need {
		out = make([]byte, 0, need)
	}
	for first := true; first || len(msg) > 0; first = false {
		chunk := msg
		if len(chunk) > limit {
			chunk = chunk[:limit]
		}
		msg = msg[len(chunk):]
		start := len(out)
		out = append(out, 0, 0)
		var err error
		out, err = b.send.Encrypt(out, nil, b.plaintext(chunk))
		if err != nil {
			return nil, err
		}
		binary.BigEndian.PutUint16(out[start:], uint16(len(out)-start-noiseLenPrefix))
	}
	if b.mask != nil {
		b.mask.w.XORKeyStream(out, out)
	}
	return out, nil
}

// Open decrypts a message sealed by the peer, in place: payload is
// overwritten, and what it returns is a slice of it.
func (b *NoiseBox) Open(payload []byte) ([]byte, error) {
	b.recvMu.Lock()
	defer b.recvMu.Unlock()
	if b.mask != nil {
		b.mask.r.XORKeyStream(payload, payload)
	}
	w := 0 // plaintext written so far, compacted to the front
	for pos := 0; pos < len(payload); {
		if len(payload)-pos < noiseLenPrefix {
			return nil, fmt.Errorf("noise: truncated record in message")
		}
		n := int(binary.BigEndian.Uint16(payload[pos:]))
		pos += noiseLenPrefix
		if n == 0 || n > len(payload)-pos {
			return nil, fmt.Errorf("noise: record of %d bytes in %d", n, len(payload)-pos)
		}
		// Decrypted where it lies (an exact overlap is allowed), then moved
		// down to follow what came before.
		ct := payload[pos : pos+n]
		plain, err := b.recv.Decrypt(ct[:0], nil, ct)
		if err != nil {
			return nil, fmt.Errorf("noise: decrypt: %w", err)
		}
		pos += n
		switch {
		case b.shape != nil:
			if len(plain) < plusPadHeader {
				return nil, fmt.Errorf("stealth+: record too short")
			}
			pad := int(binary.BigEndian.Uint16(plain))
			if plusPadHeader+pad > len(plain) {
				return nil, fmt.Errorf("stealth+: padded record does not describe itself")
			}
			plain = plain[plusPadHeader : len(plain)-pad]
		case b.pad:
			if len(plain) < 1 || 1+int(plain[0]) > len(plain) {
				return nil, fmt.Errorf("noise: padded record does not describe itself")
			}
			plain = plain[1 : len(plain)-int(plain[0])]
		}
		w += copy(payload[w:], plain)
	}
	return payload[:w], nil
}

// plaintext is what goes into one record; the caller holds sendMu, which
// makes reusing the buffer safe.
func (b *NoiseBox) plaintext(chunk []byte) []byte {
	head, pad := 0, 0
	switch {
	case b.shape != nil:
		head, pad = plusPadHeader, b.shape.pad(len(chunk))
	case b.pad:
		head, pad = 1, randPad()
	default:
		return chunk
	}
	need := head + len(chunk) + pad
	if cap(b.plain) < need {
		b.plain = make([]byte, need)
	}
	p := b.plain[:need]
	if head == 1 {
		p[0] = byte(pad)
	} else {
		binary.BigEndian.PutUint16(p, uint16(pad))
	}
	copy(p[head:], chunk)
	clear(p[head+len(chunk):])
	return p
}

// msgStream presents a message transport as the byte stream the handshake
// reads and writes: each Write is one message, and Read serves messages in
// order.
type msgStream struct {
	m   MessageTransport
	buf []byte
}

func (s *msgStream) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		_, b, err := s.m.ReadMessage()
		if err != nil {
			return 0, err
		}
		s.buf = b
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (s *msgStream) Write(p []byte) (int, error) {
	if err := s.m.WriteMessage(wsBinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *msgStream) Close() error                       { return nil }
func (s *msgStream) LocalAddr() net.Addr                { return s.m.LocalAddr() }
func (s *msgStream) RemoteAddr() net.Addr               { return s.m.RemoteAddr() }
func (s *msgStream) SetReadDeadline(t time.Time) error  { return s.m.SetReadDeadline(t) }
func (s *msgStream) SetWriteDeadline(t time.Time) error { return s.m.SetWriteDeadline(t) }
func (s *msgStream) SetDeadline(t time.Time) error {
	if err := s.m.SetReadDeadline(t); err != nil {
		return err
	}
	return s.m.SetWriteDeadline(t)
}
