package l3

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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
// packets wear, so spoof, pck, sni and xdi all get it from one implementation.
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

	sendMu    sync.Mutex
	sendGrp   uint32
	sendIdx   int
	sendPad   [][]byte // padded shards of the group being built
	sendMax   int      // the longest shard so far in this group
	sendPeer  net.Addr // where the group's parity goes, from the last write
	sendFrame []byte   // reused wire frame, under sendMu

	readMu    sync.Mutex
	readFrame []byte // receive scratch, held until the payload has been copied

	recvMu  sync.Mutex
	groups  map[uint32]*fecGroup
	order   []uint32    // group ids in arrival order, for eviction
	pending []fecReady  // rebuilt packets waiting to be read
	spare   []*fecGroup // evicted groups, kept with their shard buffers
}

// fecReady is a rebuilt packet and the address it is attributed to.
type fecReady struct {
	data []byte
	addr net.Addr
}

// fecGroup is the receiver's state for one group.
type fecGroup struct {
	shards    [][]byte // nil where not yet received; index by shard number
	store     [][]byte // the buffers shards are copied into, kept across reuse
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
	// The receiver keeps recent groups across peer reconnects. Restarting at
	// zero could therefore make its delivered bitmap discard a fresh handshake
	// as an old shard. Start in a fresh part of the existing wire ID space.
	var first [4]byte
	if _, err := rand.Read(first[:]); err != nil {
		return nil, fmt.Errorf("l3: choosing the initial FEC group: %w", err)
	}
	c := &fecCarrier{
		sendGrp: binary.BigEndian.Uint32(first[:]),
		below:   below,
		cfg:     cfg,
		enc:     enc,
		groups:  make(map[uint32]*fecGroup),
	}
	// Over a carrier that reads and writes in batches, so does this layer:
	// without it the tunnel lost recvmmsg and sendmmsg the moment FEC was on,
	// and spent a syscall on every datagram and every parity packet.
	if br, ok := below.(batchReader); ok {
		if bw, ok := below.(batchWriter); ok {
			return &fecBatchCarrier{fecCarrier: c, br: br, bw: bw}, nil
		}
	}
	return c, nil
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
	if len(p) > 65535 {
		return 0, fmt.Errorf("l3: FEC payload of %d bytes exceeds its length field", len(p))
	}
	c.sendFrame = c.stage(c.sendFrame[:0], p, addr)
	n, err := c.below.WriteTo(c.sendFrame, addr)
	if err == nil && n != len(c.sendFrame) {
		err = io.ErrShortWrite
	}
	if c.sendIdx == c.cfg.Data {
		c.parity(func() []byte { return c.sendFrame }, func(frame []byte) {
			c.sendFrame = frame
			// Data is already on the wire; parity remains best effort.
			_, _ = c.below.WriteTo(frame, c.sendPeer)
		})
	}
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// stage files p as the next data shard of the group being built and returns
// its wire frame, written into dst. The caller holds sendMu and sends the
// parity once the group is full (sendIdx == Data).
func (c *fecCarrier) stage(dst, p []byte, addr net.Addr) []byte {
	if c.sendPad == nil {
		c.sendPad = make([][]byte, c.cfg.Data+c.cfg.Parity)
	}
	// Each slot owns its shard until parity has been sent. Keep its capacity
	// across groups instead of allocating two copies per data packet.
	need := fecLenPrefix + len(p)
	shard := c.sendPad[c.sendIdx]
	if cap(shard) < need {
		shard = make([]byte, need)
	} else {
		shard = shard[:need]
	}
	binary.BigEndian.PutUint16(shard[:fecLenPrefix], uint16(len(p)))
	copy(shard[fecLenPrefix:], p)
	c.sendPad[c.sendIdx] = shard
	c.sendMax = max(c.sendMax, need)
	c.sendPeer = addr
	dst = fecGrow(dst, fecHeaderLen+need)
	putFECHeader(dst, fecKindData, c.sendGrp, byte(c.sendIdx))
	copy(dst[fecHeaderLen:], shard)
	c.sendIdx++
	return dst
}

// fecGrow is dst resized to n, keeping its capacity when it has enough.
func fecGrow(dst []byte, n int) []byte {
	if cap(dst) < n {
		return make([]byte, n)
	}
	return dst[:n]
}

// parity computes the finished group's redundancy, writes each parity frame
// into a buffer from buf and hands it to emit, then starts the next group.
// The caller holds sendMu.
func (c *fecCarrier) parity(buf func() []byte, emit func(frame []byte)) {
	defer func() {
		c.sendGrp++
		c.sendIdx = 0
		c.sendMax = 0
	}()
	size := c.sendMax
	if size == 0 {
		return
	}
	for i, shard := range c.sendPad {
		used := len(shard)
		if cap(shard) < size {
			padded := make([]byte, size)
			copy(padded, shard)
			shard = padded
		} else {
			shard = shard[:size]
			if i < c.cfg.Data {
				// Old group bytes must not become this group's padding.
				clear(shard[used:])
			}
		}
		c.sendPad[i] = shard
	}
	if err := c.enc.Encode(c.sendPad); err != nil {
		return
	}
	for i := c.cfg.Data; i < len(c.sendPad); i++ {
		frame := fecGrow(buf(), fecHeaderLen+size)
		putFECHeader(frame, fecKindParity, c.sendGrp, byte(i))
		copy(frame[fecHeaderLen:], c.sendPad[i])
		emit(frame)
	}
}

// ReadFrom returns the next datagram: a rebuilt one if any is waiting, else the
// next one off the wire. Parity packets are consumed here and never surface.
func (c *fecCarrier) ReadFrom(p []byte) (int, net.Addr, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if n, addr, ok := c.takePending(p); ok {
		return n, addr, nil
	}
	need := len(p) + fecHeaderLen + fecLenPrefix
	if cap(c.readFrame) < need {
		c.readFrame = make([]byte, need)
	}
	buf := c.readFrame[:need]
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
	c.pending[0] = fecReady{}
	c.pending = c.pending[1:]
	return copy(p, r.data), r.addr, true
}

// absorb files a shard and, when a group can be completed, queues whatever it
// rebuilds. It returns the payload to deliver now for a data shard.
func (c *fecCarrier) absorb(kind byte, group uint32, index byte, body []byte, addr net.Addr) ([]byte, bool) {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()

	total := c.cfg.Data + c.cfg.Parity
	if int(index) >= total || (kind == fecKindData && int(index) >= c.cfg.Data) ||
		(kind == fecKindParity && int(index) < c.cfg.Data) ||
		(kind != fecKindData && kind != fecKindParity) {
		return nil, false
	}
	g := c.groups[group]
	if g == nil {
		g = c.newGroup(total, addr)
		c.groups[group] = g
		c.order = append(c.order, group)
		c.evict()
	}

	if g.shards[int(index)] == nil {
		// Into the slot's own buffer: a copy per packet received was an
		// allocation per packet, and the collector set the pace.
		g.store[int(index)] = append(g.store[int(index)][:0], body...)
		g.shards[int(index)] = g.store[int(index)]
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

// evict drops the oldest groups once the window is full, keeping them to be
// reused. The caller holds recvMu.
func (c *fecCarrier) evict() {
	for len(c.order) > fecWindow {
		oldest := c.order[0]
		c.order = c.order[1:]
		if g := c.groups[oldest]; g != nil && len(c.spare) < fecWindow {
			c.spare = append(c.spare, g)
		}
		delete(c.groups, oldest)
	}
}

// newGroup is an empty group, a spare one when there is one. Rebuilt packets
// are copied out of its shards when they are queued, so nothing still points
// into a group by the time it is evicted. The caller holds recvMu.
func (c *fecCarrier) newGroup(total int, addr net.Addr) *fecGroup {
	if k := len(c.spare); k > 0 {
		g := c.spare[k-1]
		c.spare = c.spare[:k-1]
		clear(g.shards)
		clear(g.delivered)
		g.haveData, g.haveTotal, g.size, g.done, g.addr = 0, 0, 0, false, addr
		return g
	}
	return &fecGroup{
		shards:    make([][]byte, total),
		store:     make([][]byte, total),
		delivered: make([]bool, c.cfg.Data),
		addr:      addr,
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

// fecBatchCarrier is the scheme over a carrier with batch reads and writes
// (pck's and xdi's raw sockets): a batch of datagrams goes out as one batch
// with its groups' parity among it, and a batch read off the wire comes up as
// one, rebuilt packets included.
type fecBatchCarrier struct {
	*fecCarrier
	br batchReader
	bw batchWriter

	// Under sendMu: the frames of the batch being sent, and which of them are
	// data, so a short write can be reported in the caller's packets.
	wframes [][]byte
	wdata   []bool

	// Under readMu: the wire frames of a batch being read.
	rframes [][]byte
	rsizes  []int
	rfroms  []net.Addr
}

// WriteBatch sends bufs to one peer, parity after the data of each group it
// completes, and reports how many of bufs left.
func (c *fecBatchCarrier) WriteBatch(bufs [][]byte, to net.Addr) (int, error) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	k := 0
	slot := func() []byte {
		if k == len(c.wframes) {
			c.wframes = append(c.wframes, nil)
			c.wdata = append(c.wdata, false)
		}
		return c.wframes[k][:0]
	}
	put := func(frame []byte, data bool) {
		c.wframes[k], c.wdata[k] = frame, data
		k++
	}
	for _, p := range bufs {
		if len(p) > 65535 {
			return 0, fmt.Errorf("l3: FEC payload of %d bytes exceeds its length field", len(p))
		}
	}
	for _, p := range bufs {
		put(c.stage(slot(), p, to), true)
		if c.sendIdx == c.cfg.Data {
			c.parity(slot, func(frame []byte) { put(frame, false) })
		}
	}
	sent, err := c.bw.WriteBatch(c.wframes[:k], to)
	sent = min(max(sent, 0), k)
	if errors.Is(err, errNoBatch) {
		// These frames are already in their groups, so they go out as they
		// are, one at a time, rather than back to the caller to be filed
		// again.
		for _, frame := range c.wframes[sent:k] {
			_, _ = c.below.WriteTo(frame, to)
		}
		return len(bufs), nil
	}
	data := 0
	for i := 0; i < sent; i++ {
		if c.wdata[i] {
			data++
		}
	}
	if err == nil && sent < k {
		// The rest of the frames never left. Data the caller sees as sent
		// stays counted; parity is best effort.
		for i := sent; i < k; i++ {
			if c.wdata[i] {
				return data, nil
			}
		}
		return len(bufs), nil
	}
	return data, err
}

// ReadBatch reads a batch off the wire and hands up its data, and whatever
// the batch's parity rebuilt, in the order they came. It returns at least
// one datagram and never waits for a second.
func (c *fecBatchCarrier) ReadBatch(bufs [][]byte, sizes []int, froms []net.Addr) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	n := min(len(bufs), len(sizes), len(froms))
	if n == 0 {
		return 0, nil
	}
	out := c.drain(bufs[:n], sizes, froms, 0)
	if out > 0 {
		return out, nil
	}
	if len(c.rframes) < n {
		c.rframes = make([][]byte, n)
		c.rsizes = make([]int, n)
		c.rfroms = make([]net.Addr, n)
	}
	for {
		for i := 0; i < n; i++ {
			c.rframes[i] = fecGrow(c.rframes[i], len(bufs[i])+fecHeaderLen+fecLenPrefix)
		}
		got, err := c.br.ReadBatch(c.rframes[:n], c.rsizes[:n], c.rfroms[:n])
		if err != nil {
			return 0, err
		}
		for i := 0; i < got; i++ {
			kind, group, index, body, ok := parseFECHeader(c.rframes[i][:c.rsizes[i]])
			if !ok {
				continue
			}
			payload, delivered := c.absorb(kind, group, index, body, c.rfroms[i])
			if delivered {
				if out == n {
					// Every slot is full of what arrived before it; this
					// one waits with the rebuilt ones for the next read.
					c.queue(payload, c.rfroms[i])
					continue
				}
				sizes[out], froms[out] = copy(bufs[out], payload), c.rfroms[i]
				out++
			}
			// What this frame rebuilt goes up right behind it, where the
			// lost packets were, not behind the rest of the batch: TCP
			// reads a packet that turns up a batch late as reordering.
			out = c.drain(bufs[:n], sizes, froms, out)
		}
		if out > 0 {
			return out, nil
		}
	}
}

// queue holds a packet for the next read, behind any rebuilt ones already
// waiting.
func (c *fecBatchCarrier) queue(payload []byte, addr net.Addr) {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	c.pending = append(c.pending, fecReady{data: append([]byte(nil), payload...), addr: addr})
}

// drain moves rebuilt packets into bufs from slot out on, and returns the new
// count. The caller holds readMu.
func (c *fecBatchCarrier) drain(bufs [][]byte, sizes []int, froms []net.Addr, out int) int {
	for out < len(bufs) {
		n, addr, ok := c.takePending(bufs[out])
		if !ok {
			break
		}
		sizes[out], froms[out] = n, addr
		out++
	}
	return out
}
