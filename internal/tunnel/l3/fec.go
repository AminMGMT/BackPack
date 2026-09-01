package l3

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/klauspost/reedsolomon"
)

// Forward error correction, as a carrier that wraps a carrier.
//
// A layer-3 tunnel may not sit on anything that retransmits — see the package
// doc for why stacking retransmit timers makes throughput collapse rather than
// degrade. That rules out reliability; it does not rule out REDUNDANCY. For
// every group of data packets this sends a few parity packets, so a receiver
// that lost one can rebuild it from the rest without anybody waiting for a
// timer. On a path that drops a small percentage steadily — a congested
// international route, a lossy last mile — that is the difference between a
// tunnel that stutters and one that does not.
//
// It is written as a DatagramCarrier decorator rather than as part of the spoof
// carrier, for two reasons. The MTU falls out for free: Overhead() adds this
// layer's bytes to the one below, and the tunnel's existing calculation carries
// it through. And loss is a property of the path, not of the disguise the
// packets wear, so udp, spoof, pck and xdi all get it from one implementation.
//
// # What it does not do
//
//   - It does not reorder or delay. A data packet is written to the wire the
//     moment it arrives and delivered the moment it is read; parity is extra
//     traffic behind it, never a reason to hold a packet back.
//   - It does not retransmit, ever. A group with more losses than it has parity
//     is a group with a hole, and the layer above deals with that as it always
//     has.
//   - It does not deduplicate. A rebuilt packet may arrive after the original
//     did, which the tunnel's replay window above already discards — the AEAD
//     counter is exactly the right place for that, and this layer has no
//     business opening the sealed datagram to find out.

// The wire format this layer prepends to every carrier datagram:
//
//	 0        1                    5        6
//	+--------+--------------------+--------+---------------+
//	| kind   | group (uint32BE)   | index  | body          |
//	+--------+--------------------+--------+---------------+
//
// A data body is the caller's datagram behind a two-byte length; a parity body
// is one Reed-Solomon shard, which is as long as the largest shard in its group.
// The length is what lets a rebuilt shard be trimmed back to the packet it was,
// since every shard in a group is padded to a common size before the parity is
// computed over them.
const (
	fecHeaderLen = 6
	fecLenPrefix = 2

	fecKindData   byte = 0
	fecKindParity byte = 1
)

// fecOverhead is what this layer costs a data packet: its header and the length
// that precedes the payload. Parity packets are extra packets rather than extra
// bytes on an existing one, so they do not enter the MTU.
const fecOverhead = fecHeaderLen + fecLenPrefix

// fecWindow is how many recent groups a receiver keeps shards for. A group is
// only useful until its data has been delivered and its parity can no longer
// rebuild anything; holding a few is enough to cover reordering across a group
// boundary, and holding many would be memory spent on packets nobody is waiting
// for any more.
const fecWindow = 16

// FECConfig is the two numbers that describe a scheme: for every Data packets,
// Parity extra ones are sent, and any Parity of the group's packets may be lost
// without losing anything. Both zero means no FEC at all.
type FECConfig struct {
	Data   int
	Parity int
}

// Enabled reports whether a scheme was asked for. A half-configured pair is not
// a scheme — parity with nothing to protect, or data with no redundancy — so it
// reads as off rather than being quietly repaired into something the other end
// did not agree to.
func (f FECConfig) Enabled() bool { return f.Data > 0 && f.Parity > 0 }

// Validate rejects a scheme the encoder cannot build, so a configuration is
// refused where it is written rather than at the first packet.
func (f FECConfig) Validate() error {
	if !f.Enabled() {
		if f.Data != 0 || f.Parity != 0 {
			return fmt.Errorf("l3: fec needs both fec_data and fec_parity set (got %d and %d); "+
				"leave both at 0 for no error correction", f.Data, f.Parity)
		}
		return nil
	}
	if f.Data+f.Parity > 256 {
		return fmt.Errorf("l3: fec_data + fec_parity is %d; Reed-Solomon allows at most 256 shards",
			f.Data+f.Parity)
	}
	if f.Parity >= f.Data {
		return fmt.Errorf("l3: fec_parity (%d) is not smaller than fec_data (%d); that sends more "+
			"redundancy than payload, which costs more than the loss it repairs", f.Parity, f.Data)
	}
	return nil
}

// fecCarrier is the decorator. It owns the carrier below it and closes it.
type fecCarrier struct {
	below DatagramCarrier
	cfg   FECConfig
	enc   reedsolomon.Encoder

	sendMu   sync.Mutex
	sendGrp  uint32
	sendIdx  int
	sendPad  [][]byte // padded shards of the group being built
	sendMax  int      // the longest shard so far in this group
	sendPeer net.Addr // where the group's parity goes, from the last write

	recvMu  sync.Mutex
	groups  map[uint32]*fecGroup
	order   []uint32   // group ids in arrival order, for eviction
	pending []fecReady // rebuilt packets waiting to be read
}

// fecReady is a rebuilt packet and the address it is attributed to.
type fecReady struct {
	data []byte
	addr net.Addr
}

// fecGroup is the receiver's state for one group.
type fecGroup struct {
	shards    [][]byte // nil where not yet received; index by shard number
	haveData  int      // how many data shards arrived
	haveTotal int      // data + parity shards arrived
	size      int      // the padded shard size, learned from any parity shard
	delivered []bool   // data shards already handed up, so a rebuild is not re-delivered
	done      bool     // reconstruction already attempted and satisfied
	addr      net.Addr
}

// newFECCarrier wraps below in the scheme. An unusable scheme is an error here
// rather than a silent pass-through, because a tunnel whose peer is adding
// parity and whose own end is not will discard every parity packet as garbage.
func newFECCarrier(below DatagramCarrier, cfg FECConfig) (DatagramCarrier, error) {
	if !cfg.Enabled() {
		return below, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	enc, err := reedsolomon.New(cfg.Data, cfg.Parity)
	if err != nil {
		return nil, fmt.Errorf("l3: fec %d/%d: %w", cfg.Data, cfg.Parity, err)
	}
	return &fecCarrier{
		below:  below,
		cfg:    cfg,
		enc:    enc,
		groups: make(map[uint32]*fecGroup),
	}, nil
}

func (c *fecCarrier) Overhead() int { return c.below.Overhead() + fecOverhead }

func (c *fecCarrier) CarrierName() string {
	return fmt.Sprintf("%s+fec%d/%d", c.below.CarrierName(), c.cfg.Data, c.cfg.Parity)
}

func (c *fecCarrier) Close() error                       { return c.below.Close() }
func (c *fecCarrier) LocalAddr() net.Addr                { return c.below.LocalAddr() }
func (c *fecCarrier) SetDeadline(t time.Time) error      { return c.below.SetDeadline(t) }
func (c *fecCarrier) SetReadDeadline(t time.Time) error  { return c.below.SetReadDeadline(t) }
func (c *fecCarrier) SetWriteDeadline(t time.Time) error { return c.below.SetWriteDeadline(t) }

// WriteTo sends p as a data shard immediately, and — when it completes a group —
// the group's parity behind it.
//
// The data packet is never held: the parity is computed and sent after it, so
// this layer costs bandwidth and CPU but not a millisecond of latency on the
// packet the application is waiting for.
func (c *fecCarrier) WriteTo(p []byte, addr net.Addr) (int, error) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if c.sendPad == nil {
		c.sendPad = make([][]byte, c.cfg.Data+c.cfg.Parity)
	}
	// The shard this packet becomes: its length, then its bytes. It is kept for
	// the parity computation and padded out once the group's size is known.
	shard := make([]byte, fecLenPrefix+len(p))
	binary.BigEndian.PutUint16(shard[:fecLenPrefix], uint16(len(p)))
	copy(shard[fecLenPrefix:], p)
	c.sendPad[c.sendIdx] = shard
	if len(shard) > c.sendMax {
		c.sendMax = len(shard)
	}
	c.sendPeer = addr

	// On the wire the data shard travels unpadded — the padding is only a device
	// for computing parity, and sending it would waste the bandwidth this layer
	// exists to spend well.
	out := make([]byte, fecHeaderLen+len(shard))
	putFECHeader(out, fecKindData, c.sendGrp, byte(c.sendIdx))
	copy(out[fecHeaderLen:], shard)
	n, err := c.below.WriteTo(out, addr)

	c.sendIdx++
	if c.sendIdx == c.cfg.Data {
		// The group is full: pad every shard to the common size, compute the
		// parity over them, and send it. A failure here is not the data
		// packet's failure — that one is already gone — so it is dropped
		// rather than reported: the group simply has no protection.
		c.flushParity()
	}
	if err != nil {
		return 0, err
	}
	// Report what the caller handed us, not what went on the wire.
	if n >= fecHeaderLen+fecLenPrefix {
		return len(p), nil
	}
	return len(p), nil
}

// flushParity computes and sends the current group's parity, then starts the
// next group. The caller holds sendMu.
func (c *fecCarrier) flushParity() {
	defer func() {
		c.sendGrp++
		c.sendIdx = 0
		c.sendMax = 0
		c.sendPad = nil
	}()

	size := c.sendMax
	if size == 0 {
		return
	}
	shards := make([][]byte, c.cfg.Data+c.cfg.Parity)
	for i := 0; i < c.cfg.Data; i++ {
		padded := make([]byte, size)
		copy(padded, c.sendPad[i])
		shards[i] = padded
	}
	for i := c.cfg.Data; i < len(shards); i++ {
		shards[i] = make([]byte, size)
	}
	if err := c.enc.Encode(shards); err != nil {
		return
	}
	for i := c.cfg.Data; i < len(shards); i++ {
		out := make([]byte, fecHeaderLen+size)
		putFECHeader(out, fecKindParity, c.sendGrp, byte(i))
		copy(out[fecHeaderLen:], shards[i])
		// Best effort: a parity packet that cannot be sent costs the group its
		// protection and nothing else.
		_, _ = c.below.WriteTo(out, c.sendPeer)
	}
}

// ReadFrom returns the next datagram: a rebuilt one if any is waiting, else the
// next one off the wire. Parity packets are consumed here and never surface.
func (c *fecCarrier) ReadFrom(p []byte) (int, net.Addr, error) {
	if n, addr, ok := c.takePending(p); ok {
		return n, addr, nil
	}
	buf := make([]byte, len(p)+fecHeaderLen+fecLenPrefix)
	for {
		n, addr, err := c.below.ReadFrom(buf)
		if err != nil {
			return 0, nil, err
		}
		kind, group, index, body, ok := parseFECHeader(buf[:n])
		if !ok {
			continue // not ours, or truncated: the layer above never sees it
		}
		payload, delivered := c.absorb(kind, group, index, body, addr)
		if !delivered {
			// A parity packet, or a duplicate. It may have completed a group,
			// so check the rebuilt queue before going back to the wire.
			if n, a, ok := c.takePending(p); ok {
				return n, a, nil
			}
			continue
		}
		return copy(p, payload), addr, nil
	}
}

// takePending pops one rebuilt packet, if there is one.
func (c *fecCarrier) takePending(p []byte) (int, net.Addr, bool) {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	if len(c.pending) == 0 {
		return 0, nil, false
	}
	r := c.pending[0]
	c.pending = c.pending[1:]
	return copy(p, r.data), r.addr, true
}

// absorb files a shard and, when a group can be completed, queues whatever it
// rebuilds. It returns the payload to deliver now for a data shard.
func (c *fecCarrier) absorb(kind byte, group uint32, index byte, body []byte, addr net.Addr) ([]byte, bool) {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()

	total := c.cfg.Data + c.cfg.Parity
	if int(index) >= total {
		return nil, false
	}
	g := c.groups[group]
	if g == nil {
		g = &fecGroup{
			shards:    make([][]byte, total),
			delivered: make([]bool, c.cfg.Data),
			addr:      addr,
		}
		c.groups[group] = g
		c.order = append(c.order, group)
		c.evict()
	}

	if g.shards[int(index)] == nil {
		g.shards[int(index)] = append([]byte(nil), body...)
		g.haveTotal++
		if int(index) < c.cfg.Data {
			g.haveData++
		}
		// Every parity shard is full size, so any one of them tells the
		// receiver how wide this group's shards are.
		if kind == fecKindParity && g.size == 0 {
			g.size = len(body)
		}
	}

	var out []byte
	var deliver bool
	if kind == fecKindData && !g.delivered[int(index)] {
		g.delivered[int(index)] = true
		if payload, ok := trimShard(body); ok {
			out, deliver = payload, true
		}
	}

	c.rebuild(g)
	return out, deliver
}

// rebuild fills a group's holes from its parity, when there is enough to do it,
// and queues the data packets that were missing. The caller holds recvMu.
func (c *fecCarrier) rebuild(g *fecGroup) {
	if g.done || g.haveData == c.cfg.Data || g.haveTotal < c.cfg.Data || g.size == 0 {
		return
	}
	// Reed-Solomon needs every present shard at the common width; the data
	// shards travelled unpadded, so they are widened back here.
	work := make([][]byte, len(g.shards))
	for i, s := range g.shards {
		if s == nil {
			continue
		}
		if len(s) == g.size {
			work[i] = s
			continue
		}
		if len(s) > g.size {
			return // a shard wider than the group: not a group we can rebuild
		}
		padded := make([]byte, g.size)
		copy(padded, s)
		work[i] = padded
	}
	if err := c.enc.Reconstruct(work); err != nil {
		return
	}
	g.done = true
	for i := 0; i < c.cfg.Data; i++ {
		if g.delivered[i] || work[i] == nil {
			continue
		}
		if payload, ok := trimShard(work[i]); ok {
			g.delivered[i] = true
			c.pending = append(c.pending, fecReady{data: append([]byte(nil), payload...), addr: g.addr})
		}
	}
}

// evict drops the oldest groups once the window is full. The caller holds
// recvMu.
func (c *fecCarrier) evict() {
	for len(c.order) > fecWindow {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.groups, oldest)
	}
}

// putFECHeader writes the fixed prefix.
func putFECHeader(dst []byte, kind byte, group uint32, index byte) {
	_ = dst[fecHeaderLen-1]
	dst[0] = kind
	binary.BigEndian.PutUint32(dst[1:5], group)
	dst[5] = index
}

// parseFECHeader splits a received datagram. ok is false for anything too short
// or carrying a kind this layer does not write, which is how a packet from
// something that is not this tunnel is dropped before it can confuse a group.
func parseFECHeader(buf []byte) (kind byte, group uint32, index byte, body []byte, ok bool) {
	if len(buf) < fecHeaderLen {
		return 0, 0, 0, nil, false
	}
	kind = buf[0]
	if kind != fecKindData && kind != fecKindParity {
		return 0, 0, 0, nil, false
	}
	return kind, binary.BigEndian.Uint32(buf[1:5]), buf[5], buf[fecHeaderLen:], true
}

// trimShard reads the length a shard was padded from and returns the packet.
// ok is false for a shard whose length does not fit inside it, which is what a
// corrupt or mis-sized rebuild looks like.
func trimShard(shard []byte) ([]byte, bool) {
	if len(shard) < fecLenPrefix {
		return nil, false
	}
	n := int(binary.BigEndian.Uint16(shard[:fecLenPrefix]))
	if n > len(shard)-fecLenPrefix {
		return nil, false
	}
	return shard[fecLenPrefix : fecLenPrefix+n], true
}
