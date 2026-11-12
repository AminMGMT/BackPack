package transport

import (
	"encoding/binary"
	"sync"

	"github.com/backpack/backpack/internal/utils/network"
)

// datagramBatch is the queue between the goroutine that reads a UDP backend and
// the one that writes its replies into the tunnel stream.
//
// A stream write returns only once the frame has been handed to the carrier, and
// on a loaded session that takes tens of milliseconds. With one write per
// datagram a flow could not move more than that many datagrams a second, and
// the rest waited in the socket's receive buffer — seconds of them, then
// dropped from the newest end. Here the writer takes everything that has queued
// up while it was busy and writes it as one piece, so the write rate stops
// limiting the datagram rate, and when the tunnel really is slower than the
// backend the oldest datagrams are the ones dropped.
type datagramBatch struct {
	mu     sync.Mutex
	cond   *sync.Cond
	frames [][]byte // each: 2-byte length + payload
	max    int
	closed bool
	// dropped counts datagrams discarded because the queue was full.
	dropped uint64
}

func newDatagramBatch(max int) *datagramBatch {
	b := &datagramBatch{max: max}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// put queues one datagram, dropping the oldest if the queue is full. It never
// blocks.
func (b *datagramBatch) put(payload []byte) {
	frame := network.GetDatagramBuffer(2 + len(payload))
	binary.BigEndian.PutUint16(frame, uint16(len(payload)))
	copy(frame[2:], payload)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		network.PutDatagramBuffer(frame)
		return
	}
	if len(b.frames) >= b.max {
		network.PutDatagramBuffer(b.frames[0])
		b.frames[0] = nil
		b.frames = b.frames[1:]
		b.dropped++
	}
	b.frames = append(b.frames, frame)
	b.mu.Unlock()
	b.cond.Signal()
}

// take blocks until something is queued, then appends as many whole queued
// frames as fit in limit bytes (at least one) to dst[:0] and returns it. It
// returns false once the batch is closed.
func (b *datagramBatch) take(dst []byte, limit int) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(b.frames) == 0 && !b.closed {
		b.cond.Wait()
	}
	if b.closed {
		return dst, false
	}
	dst = dst[:0]
	n := 0
	for n < len(b.frames) {
		f := b.frames[n]
		if n > 0 && len(dst)+len(f) > limit {
			break
		}
		dst = append(dst, f...)
		network.PutDatagramBuffer(f)
		b.frames[n] = nil
		n++
	}
	b.frames = b.frames[n:]
	if len(b.frames) == 0 {
		b.frames = nil
	}
	return dst, true
}

func (b *datagramBatch) close() {
	b.mu.Lock()
	b.closed = true
	for i, f := range b.frames {
		network.PutDatagramBuffer(f)
		b.frames[i] = nil
	}
	b.frames = nil
	b.mu.Unlock()
	b.cond.Broadcast()
}
