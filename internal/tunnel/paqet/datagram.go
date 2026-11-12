package paqet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// The datagram lane.
//
// A forwarded UDP flow used to be a stream of the session, its datagrams
// length-framed into the byte stream. That made them reliable, ordered and
// queued behind everything else on the session, which is the opposite of what
// a UDP application asked for. On a congested tunnel the soak harness measured
// 7.8% of the datagrams delivered with a median round trip of 11.7 seconds:
// they waited their turn in smux's round robin with hundreds of short
// connections and a few bulk transfers, then in KCP's window, and a loss
// anywhere held up every datagram behind it. A resolver, a game, a call or a
// QUIC connection inside the tunnel is better served by a datagram that is
// dropped than by one that arrives late, and the application above it already
// has the retransmission it wants (DNS, QUIC) or does not want one at all.
//
// So a datagram that fits one segment travels beside KCP, not in it, as a
// TCP segment of the same flow (the same source and destination port, so a
// middlebox sees one flow, not two):
//
//	+-----+-----------+--------------------------------+-----------+
//	| tag | nonce (10)| AES-256-GCM( flow (4) | data ) | GCM tag 16|
//	+-----+-----------+--------------------------------+-----------+
//	  2 B                                                  = 32 bytes of overhead
//
// It is sealed with a key of its own, derived from the tunnel's, and carries the
// id of the smux stream that opened the flow. The stream stays: it is how the
// far end is asked to dial the target, how a refusal comes back, how the flow
// is closed and how both ends know it has gone idle; only the datagrams
// themselves leave it. A datagram too large for one segment, and every datagram
// sent before the target's answer has come back (the far end may not know the
// flow yet, and the datagram would be lost for it), still use the stream, as
// before.
//
// # Telling a datagram from a KCP packet
//
// KCP's packets are encrypted from their first byte, so they carry no marker,
// and a plain one on the datagrams would be a fixed fingerprint. The first two
// bytes are instead a keyed function of the next ten, which the receiver
// recomputes: one AES block, about 20 ns. A KCP packet passes that check once
// in 65 536; the datagram is then opened, which fails, and the packet goes to KCP
// as it would have. A real datagram never reaches KCP.
//
// # What it is not
//
// It is not reliable, not ordered, not rate controlled and not queued: the
// forwarder's write is the send, a full local transmit queue drops it, and the
// path may drop it. A flow that offers more than the path carries gets the loss
// a plain UDP flow would get. Its bytes do count in the path's queue, so the
// window control (congestion.go) sees them and gives way, which is the
// sharing a UDP flow ought to have with the TCP traffic beside it.

const (
	dgTagLen   = 2
	dgNonceLen = 10 // after the tag
	dgGCMNonce = dgTagLen + dgNonceLen
	dgFlowLen  = 4
	dgOverhead = dgGCMNonce + dgFlowLen + 16

	// dgQueue is how many datagrams a flow holds for its reader. A reader that
	// falls behind loses the newest, as a UDP socket's full buffer does.
	dgQueue = 256
)

// dgCodec seals and opens the lane's packets.
type dgCodec struct {
	aead cipher.AEAD
	tag  cipher.Block
	role byte
	ctr  atomic.Uint64
}

// newDGCodec derives the lane's keys from the tunnel's key material. role
// keeps the two directions' nonces apart under the one key.
func newDGCodec(key []byte, server bool) (*dgCodec, error) {
	k1 := sha256.Sum256(append(append([]byte(nil), key...), "backpack-pck-dgram-aead"...))
	k2 := sha256.Sum256(append(append([]byte(nil), key...), "backpack-pck-dgram-tag"...))
	blk, err := aes.NewCipher(k1[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	tag, err := aes.NewCipher(k2[:16])
	if err != nil {
		return nil, err
	}
	c := &dgCodec{aead: aead, tag: tag, role: 1}
	if server {
		c.role = 2
	}
	c.ctr.Store(rand.Uint64() >> 8)
	return c, nil
}

// tagOf is the two-byte marker for a ten-byte nonce.
func (c *dgCodec) tagOf(nonce []byte) (byte, byte) {
	var in, out [16]byte
	copy(in[:], nonce[:dgNonceLen])
	c.tag.Encrypt(out[:], in[:])
	return out[0], out[1]
}

// seal returns the packet for one datagram of a flow.
func (c *dgCodec) seal(flow uint32, data []byte) []byte {
	buf := make([]byte, dgGCMNonce+dgFlowLen+len(data), dgOverhead+len(data))
	n := c.ctr.Add(1)
	nonce := buf[dgTagLen:dgGCMNonce]
	nonce[0] = c.role
	binary.BigEndian.PutUint64(nonce[1:], n<<8|uint64(rand.Uint32()&0xff))
	nonce[dgNonceLen-1] = byte(rand.Uint32())
	buf[0], buf[1] = c.tagOf(nonce)
	binary.BigEndian.PutUint32(buf[dgGCMNonce:], flow)
	copy(buf[dgGCMNonce+dgFlowLen:], data)
	return c.aead.Seal(buf[:dgGCMNonce], buf[:dgGCMNonce], buf[dgGCMNonce:], nil)
}

// open returns the flow and datagram of a packet, or false when it is not one
// of the lane's. scratch is where the plaintext goes (the packet is left
// intact: a failed open must hand it on to KCP); it must hold the packet.
func (c *dgCodec) open(pkt, scratch []byte) (flow uint32, data []byte, ok bool) {
	if len(pkt) < dgOverhead {
		return 0, nil, false
	}
	t0, t1 := c.tagOf(pkt[dgTagLen:dgGCMNonce])
	if pkt[0] != t0 || pkt[1] != t1 {
		return 0, nil, false
	}
	plain, err := c.aead.Open(scratch[:0], pkt[:dgGCMNonce], pkt[dgGCMNonce:], nil)
	if err != nil || len(plain) < dgFlowLen {
		return 0, nil, false
	}
	return binary.BigEndian.Uint32(plain), plain[dgFlowLen:], true
}

// dgLane is a session's datagram lane: how to send on it and who is waiting
// for what arrives.
type dgLane struct {
	codec  *dgCodec
	sock   *Socket
	remote *net.UDPAddr
	// max is the largest datagram one segment carries.
	max int
	mux dgMux

	engine *Engine // for the counters; may be nil
}

func newDGLane(codec *dgCodec, sock *Socket, remote net.Addr, mtu int) *dgLane {
	ua, ok := remote.(*net.UDPAddr)
	if !ok || codec == nil || sock == nil {
		return nil
	}
	return &dgLane{codec: codec, sock: sock, remote: ua, max: mtu - dgOverhead}
}

// send puts one datagram of flow on the wire. It reports false, having sent
// nothing, when the datagram does not fit a segment.
func (l *dgLane) send(flow uint32, data []byte) bool {
	if l == nil || len(data) > l.max {
		return false
	}
	l.sock.WriteTo(l.codec.seal(flow, data), l.remote)
	if l.engine != nil {
		l.engine.dgOut.Add(1)
	}
	return true
}

// receive hands an arriving datagram to its flow. A datagram for a flow that
// is not (or no longer) open is dropped, as UDP to a closed port is.
func (l *dgLane) receive(flow uint32, data []byte) {
	f := l.mux.get(flow)
	if f == nil {
		return
	}
	d := make([]byte, len(data))
	copy(d, data)
	if f.push(d) {
		if l.engine != nil {
			l.engine.dgIn.Add(1)
		}
	} else if l.engine != nil {
		l.engine.dgDrop.Add(1)
	}
}

// dgMux is a session's open flows by stream id.
type dgMux struct {
	mu    sync.RWMutex
	flows map[uint32]*dgFlow
}

func (m *dgMux) add(id uint32, f *dgFlow) {
	m.mu.Lock()
	if m.flows == nil {
		m.flows = map[uint32]*dgFlow{}
	}
	m.flows[id] = f
	m.mu.Unlock()
}

func (m *dgMux) get(id uint32) *dgFlow {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.flows[id]
}

func (m *dgMux) remove(id uint32) {
	m.mu.Lock()
	delete(m.flows, id)
	m.mu.Unlock()
}

// dgFlow is what one flow's datagrams wait in, whichever way they arrived (the
// lane, or the stream for the ones that did not fit).
type dgFlow struct {
	in   chan []byte
	done chan struct{}
	once sync.Once
	err  error // set before done closes
}

func newDGFlow() *dgFlow {
	return &dgFlow{in: make(chan []byte, dgQueue), done: make(chan struct{})}
}

// push queues a datagram without waiting; false when the queue is full.
func (f *dgFlow) push(d []byte) bool {
	select {
	case f.in <- d:
		return true
	default:
		return false
	}
}

// pushWait queues a datagram, waiting for room; false when the flow ended.
func (f *dgFlow) pushWait(d []byte) bool {
	select {
	case f.in <- d:
		return true
	case <-f.done:
		return false
	}
}

// fail ends the flow with err, which its reader sees once the queue is empty.
func (f *dgFlow) fail(err error) {
	f.once.Do(func() { f.err = err; close(f.done) })
}

// read returns the next datagram into p (one longer than p is dropped whole,
// as the stream framing always did), waiting until deadline if it is not zero.
func (f *dgFlow) read(p []byte, deadline time.Time) (int, error) {
	for {
		select {
		case d := <-f.in:
			if len(d) <= len(p) {
				return copy(p, d), nil
			}
			continue
		default:
		}
		var tc <-chan time.Time
		var timer *time.Timer
		if !deadline.IsZero() {
			wait := time.Until(deadline)
			if wait <= 0 {
				return 0, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(wait)
			tc = timer.C
		}
		select {
		case d := <-f.in:
			if timer != nil {
				timer.Stop()
			}
			if len(d) <= len(p) {
				return copy(p, d), nil
			}
		case <-f.done:
			if timer != nil {
				timer.Stop()
			}
			select {
			case d := <-f.in:
				if len(d) <= len(p) {
					return copy(p, d), nil
				}
			default:
			}
			return 0, f.err
		case <-tc:
			return 0, os.ErrDeadlineExceeded
		}
	}
}
