package network

import (
	"net"
	"sync"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// Reading and sending xdi echoes several at a time.
//
// The same reasoning as the pck carrier (pckbatch_linux.go): the layer-3
// tunnel reads its carrier in batches and writes the batch to its interface in
// one call, which is what lets the device coalesce a flow's segments, and it
// seals a batch from the interface in one loop. The raw ICMP socket did both a
// packet at a time, and sending was 45% of a loaded xdi tunnel's CPU.

type icmpBatch struct {
	once sync.Once
	p4   *ipv4.PacketConn // nil when the socket is not a real one (tests)

	rmu  sync.Mutex
	rmsg []ipv4.Message

	wmu    sync.Mutex
	wmsg   []ipv4.Message
	frames [][]byte
}

func (c *icmpConn) batchConn() *ipv4.PacketConn {
	c.batch.once.Do(func() {
		if pc, ok := c.pc.(*icmp.PacketConn); ok {
			c.batch.p4 = pc.IPv4PacketConn()
		}
	})
	return c.batch.p4
}

// ReadBatch reads up to len(bufs) of this tunnel's echoes, leaving each one's
// payload at the front of bufs[i]. It blocks until one is this tunnel's and
// never waits for a second.
func (c *icmpConn) ReadBatch(bufs [][]byte, sizes []int, froms []net.Addr) (int, error) {
	p4 := c.batchConn()
	if p4 == nil {
		return 0, errPckNoBatch
	}
	n := len(bufs)
	if len(sizes) < n {
		n = len(sizes)
	}
	if len(froms) < n {
		n = len(froms)
	}
	if n == 0 {
		return 0, nil
	}

	c.batch.rmu.Lock()
	defer c.batch.rmu.Unlock()
	if len(c.batch.rmsg) < n {
		c.batch.rmsg = make([]ipv4.Message, n)
	}
	msgs := c.batch.rmsg[:n]
	wantType, wantDir := icmpEchoRequest, inboundDir(c.server)
	if !c.server {
		wantType = icmpEchoReply
	}

	for {
		for i := range msgs {
			msgs[i].Buffers = [][]byte{bufs[i]}
			msgs[i].N, msgs[i].Addr = 0, nil
		}
		got, err := p4.ReadBatch(msgs, 0)
		if err != nil {
			return 0, err
		}
		out := 0
		for i := 0; i < got; i++ {
			pkt := bufs[i][:msgs[i].N]
			// A raw socket read by recvmmsg carries the IP header, where the
			// single read had it stripped by the net package.
			if len(pkt) >= 20 && pkt[0]>>4 == 4 {
				if ihl := int(pkt[0]&0x0f) * 4; ihl >= 20 && ihl <= len(pkt) {
					pkt = pkt[ihl:]
				}
			}
			if len(pkt) < 8 || pkt[0] != byte(wantType) {
				continue
			}
			echoID := int(pkt[4])<<8 | int(pkt[5])
			if !c.server && echoID != int(c.id) {
				continue
			}
			payload, ok := decodeXdiPayload(c.tag, wantDir, pkt[8:])
			if !ok {
				continue
			}
			sizes[out] = copy(bufs[out], payload)
			froms[out] = c.peerAddr(msgs[i].Addr, echoID)
			out++
		}
		if out > 0 {
			return out, nil
		}
	}
}

// WriteBatch sends bufs to one peer as consecutive echoes.
func (c *icmpConn) WriteBatch(bufs [][]byte, to net.Addr) (int, error) {
	p4 := c.batchConn()
	if p4 == nil {
		return 0, errPckNoBatch
	}
	ipAddr, err := toIPAddr(to)
	if err != nil {
		return 0, err
	}
	n := len(bufs)
	if n == 0 {
		return 0, nil
	}
	typ := icmpEchoRequest
	if c.server {
		typ = icmpEchoReply
	}
	id := uint16(c.echoIDFor(to))

	c.batch.wmu.Lock()
	defer c.batch.wmu.Unlock()
	if len(c.batch.wmsg) < n {
		c.batch.wmsg = make([]ipv4.Message, n)
		frames := make([][]byte, n)
		copy(frames, c.batch.frames)
		c.batch.frames = frames
	}
	msgs := c.batch.wmsg[:n]
	for i, p := range bufs {
		frame := appendEcho(c.batch.frames[i][:0], byte(typ), id, uint16(c.seq.Add(1)))
		frame = appendXdiPayload(frame, c.tag, outboundDir(c.server), p)
		setICMPChecksum(frame)
		c.batch.frames[i] = frame
		msgs[i].Buffers = [][]byte{frame}
		msgs[i].Addr = ipAddr
	}

	sent := 0
	for sent < n {
		k, err := p4.WriteBatch(msgs[sent:], 0)
		if err != nil {
			return sent, err
		}
		if k == 0 {
			break
		}
		sent += k
	}
	return sent, nil
}
