package network

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/flynn/noise"
	"golang.org/x/crypto/chacha20"
	"golang.org/x/crypto/hkdf"
)

// Stealth+: stealth with nothing left on the wire to parse.
//
// Stealth's records are encrypted, but two things around them are not: the
// two-byte length in front of every record, and the handshake's ephemeral key,
// which is an X25519 point and not quite a uniformly random string. Both are
// structure, and structure is what a classifier starts from. Stealth+ sends
// every byte of the connection — handshake, lengths, records — through a
// stream cipher keyed from the token, with a random nonce each way, so the
// whole connection is indistinguishable from random bytes from its first.
//
// It also shapes what length does show. With the lengths masked an observer
// sees only TCP segment sizes, and those say something only for small writes:
// a bulk transfer fills segments whatever it carries. So a record shorter than
// plusShapeMax is padded to a length drawn from a distribution of the
// tunnel's own (seeded from the token, so every tunnel's differs and both ends
// of one agree), and a longer one gets the light filler stealth gives. The
// handshake messages carry random filler too.
//
// What it costs: one more stream-cipher pass over every byte, and filler on
// small records. Bulk throughput is stealth's.

const (
	plusPrologue = "backpack-stealth-plus-v1"
	// plusShapeMax: records shorter than this are shaped.
	plusShapeMax = 1400
	// plusPadHeader is the padded record's filler length, two bytes: shaped
	// filler can exceed one.
	plusPadHeader = 2
	// plusMaxPayload is the most data one record carries.
	plusMaxPayload = noiseMaxPayload - plusPadHeader - plusShapeMax
	plusNonce      = 12
)

func plusKey(token, label string, n int) []byte {
	k := make([]byte, n)
	r := hkdf.New(sha256.New, []byte(token), []byte(label), nil)
	if _, err := io.ReadFull(r, k); err != nil {
		panic(err) // hkdf over sha256 yields far more than n bytes
	}
	return k
}

// StealthPlusClientConn runs the kharej's half of stealth+ over raw.
func StealthPlusClientConn(raw net.Conn, token string, timeout time.Duration) (net.Conn, error) {
	return stealthPlus(raw, token, true, timeout)
}

// StealthPlusServerConn runs the Iran server's half.
func StealthPlusServerConn(raw net.Conn, token string, timeout time.Duration) (net.Conn, error) {
	return stealthPlus(raw, token, false, timeout)
}

func stealthPlus(raw net.Conn, token string, initiator bool, timeout time.Duration) (net.Conn, error) {
	m := newMaskConn(raw, token)
	c, err := noiseHandshakeWith(m, token, initiator, timeout, noiseOpts{prologue: plusPrologue, hsPad: true})
	if err != nil {
		return nil, err
	}
	nc := c.(*noiseConn)
	return &plusConn{Conn: m, send: nc.send, recv: nc.recv, shape: newPlusShape(token)}, nil
}

// maskConn XORs everything each way with a ChaCha20 keystream keyed from the
// token; each direction opens with its random nonce.
type maskConn struct {
	net.Conn
	key []byte

	wmu sync.Mutex
	w   *chacha20.Cipher
	wb  []byte

	rmu sync.Mutex
	r   *chacha20.Cipher
}

func newMaskConn(raw net.Conn, token string) *maskConn {
	return &maskConn{Conn: raw, key: plusKey(token, "backpack-stealth-plus-mask-v1", chacha20.KeySize)}
}

// NetConn is the connection under the mask.
func (c *maskConn) NetConn() net.Conn { return c.Conn }

func (c *maskConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	head := 0
	if c.w == nil {
		head = plusNonce
	}
	if cap(c.wb) < head+len(p) {
		c.wb = make([]byte, head+len(p))
	}
	buf := c.wb[:head+len(p)]
	if c.w == nil {
		if _, err := rand.Read(buf[:plusNonce]); err != nil {
			return 0, err
		}
		w, err := chacha20.NewUnauthenticatedCipher(c.key, buf[:plusNonce])
		if err != nil {
			return 0, err
		}
		c.w = w
	}
	c.w.XORKeyStream(buf[head:], p)
	if _, err := c.Conn.Write(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *maskConn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	if c.r == nil {
		var nonce [plusNonce]byte
		if _, err := io.ReadFull(c.Conn, nonce[:]); err != nil {
			return 0, err
		}
		r, err := chacha20.NewUnauthenticatedCipher(c.key, nonce[:])
		if err != nil {
			return 0, err
		}
		c.r = r
	}
	n, err := c.Conn.Read(p)
	c.r.XORKeyStream(p[:n], p[:n])
	return n, err
}

// plusShape is a tunnel's record-length distribution: a few lengths, each
// with a weight, drawn once from the token.
type plusShape struct {
	lens    []int
	weights []int
}

func newPlusShape(token string) *plusShape {
	seed := plusKey(token, "backpack-stealth-plus-shape-v1", 16)
	r := mrand.New(mrand.NewPCG(binary.LittleEndian.Uint64(seed), binary.LittleEndian.Uint64(seed[8:])))
	n := 6 + r.IntN(5)
	s := &plusShape{}
	for range n {
		s.lens = append(s.lens, 96+r.IntN(plusShapeMax-96))
	}
	slices.Sort(s.lens)
	for range n {
		s.weights = append(s.weights, 1+r.IntN(9))
	}
	return s
}

// pad is the filler for a record of n bytes of data.
func (s *plusShape) pad(n int) int {
	if n >= plusShapeMax {
		return mrand.IntN(noiseMaxPad + 1)
	}
	total := 0
	for i, l := range s.lens {
		if l >= n {
			total += s.weights[i]
		}
	}
	target := plusShapeMax
	if total > 0 {
		pick := mrand.IntN(total)
		for i, l := range s.lens {
			if l < n {
				continue
			}
			if pick -= s.weights[i]; pick < 0 {
				target = l
				break
			}
		}
	}
	return target - n + mrand.IntN(32)
}

// plusConn is the record layer: stealth's, with a two-byte filler length and
// shaped filler, over the mask.
type plusConn struct {
	net.Conn // the maskConn
	shape    *plusShape

	writeMu sync.Mutex
	send    *noise.CipherState
	plain   []byte
	frame   []byte

	readMu  sync.Mutex
	recv    *noise.CipherState
	readBuf []byte
	inBuf   []byte
}

// NetConn is the socket under the record layer and the mask.
func (c *plusConn) NetConn() net.Conn { return c.Conn }

func (c *plusConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	total := 0
	for first := true; first || len(p) > 0; first = false {
		chunk := p
		if len(chunk) > plusMaxPayload {
			chunk = chunk[:plusMaxPayload]
		}
		pad := c.shape.pad(len(chunk))
		need := plusPadHeader + len(chunk) + pad
		if cap(c.plain) < need {
			c.plain = make([]byte, need)
		}
		c.plain = c.plain[:need]
		binary.BigEndian.PutUint16(c.plain, uint16(pad))
		copy(c.plain[plusPadHeader:], chunk)
		clear(c.plain[plusPadHeader+len(chunk):])
		c.frame = append(c.frame[:0], 0, 0)
		frame, err := c.send.Encrypt(c.frame, nil, c.plain)
		if err != nil {
			return total, err
		}
		c.frame = frame
		binary.BigEndian.PutUint16(frame, uint16(len(frame)-noiseLenPrefix))
		if _, err := c.Conn.Write(frame); err != nil {
			return total, err
		}
		total += len(chunk)
		p = p[len(chunk):]
		if len(p) == 0 {
			break
		}
	}
	return total, nil
}

func (c *plusConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for len(c.readBuf) == 0 {
		frame, err := readNoiseFrameInto(c.Conn, c.inBuf)
		if err != nil {
			return 0, err
		}
		c.inBuf = frame
		plain, err := c.recv.Decrypt(frame[:0], nil, frame)
		if err != nil {
			return 0, fmt.Errorf("stealth+: record failed authentication: %w", err)
		}
		if len(plain) < plusPadHeader {
			return 0, fmt.Errorf("stealth+: record too short")
		}
		pad := int(binary.BigEndian.Uint16(plain))
		if plusPadHeader+pad > len(plain) {
			return 0, fmt.Errorf("stealth+: record claims %d bytes of filler in %d", pad, len(plain))
		}
		c.readBuf = plain[plusPadHeader : len(plain)-pad]
	}
	n := copy(p, c.readBuf)
	c.readBuf = c.readBuf[n:]
	return n, nil
}
