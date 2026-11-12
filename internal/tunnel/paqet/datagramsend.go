package paqet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Sending a forwarded UDP flow without making the sender wait.
//
// A UDP flow is a stream of the session like any other, and a write on a smux
// stream returns only once its frame has gone into KCP: behind every other
// stream's frame in smux's round robin, and behind KCP's window. On a loaded
// tunnel that is a long wait — the soak run measured half a second at 100 Mbit/s
// with bulk transfers and a few hundred short connections sharing the session.
// Each datagram used to be written that way, one at a time, by the loop that
// reads the flow's socket (the forwarder's on the dialling side, the relay's on
// the listening side). So a flow could send about two datagrams per round of
// the queue, the socket's buffer filled behind it, and the kernel dropped the
// rest: 93% of the datagrams of four 50-packet-per-second flows were lost, with
// the ones that arrived 9 seconds late, where the same path without the tunnel
// lost 2%.
//
// So a flow's datagrams are queued, and one goroutine per flow writes whatever
// has queued up in a single stream write. The reader never waits on the
// tunnel; however long one round of the queue takes, everything that arrived
// meanwhile crosses in the next one. The queue is bounded, and a datagram that
// does not fit is dropped and counted — which is what a UDP socket does when
// its reader falls behind, and the one place a loss is honest here: the flow is
// offering more than its share of the tunnel.

// datagramQueueBytes is the most one flow may have waiting to be written,
// headers included. With a round of the session's queue at half a second that
// is 16 Mbit/s for one flow before anything is dropped; on a tunnel that is not
// saturated a round is milliseconds, and the queue is empty.
const datagramQueueBytes = 1 << 20

// keepBatchCap is the largest buffer kept for reuse between writes. A burst
// that needed more is let go once written, so an idle flow holds nothing large.
const keepBatchCap = 64 << 10

var errDatagramTooLarge = errors.New("paqet: datagram does not fit a frame")

// datagramSender writes framed datagrams to w from a goroutine of its own.
type datagramSender struct {
	w       io.Writer
	dropped *atomic.Uint64 // the engine's count; may be nil

	mu      sync.Mutex
	pending []byte // framed datagrams not yet handed to w
	spare   []byte
	err     error // the write that failed; every send after it fails

	wake      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newDatagramSender(w io.Writer, dropped *atomic.Uint64) *datagramSender {
	s := &datagramSender{w: w, dropped: dropped, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go s.run()
	return s
}

// send queues one datagram and returns at once. It fails only once a write
// has failed — the stream is gone — and drops the datagram, returning nil,
// when the flow already has a full queue.
func (s *datagramSender) send(d []byte) error {
	if len(d) > maxDatagram {
		return fmt.Errorf("%w: %d bytes", errDatagramTooLarge, len(d))
	}
	s.mu.Lock()
	if s.err != nil {
		err := s.err
		s.mu.Unlock()
		return err
	}
	if len(s.pending)+2+len(d) > datagramQueueBytes {
		s.mu.Unlock()
		if s.dropped != nil {
			s.dropped.Add(1)
		}
		return nil
	}
	s.pending = binary.BigEndian.AppendUint16(s.pending, uint16(len(d)))
	s.pending = append(s.pending, d...)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *datagramSender) run() {
	for {
		select {
		case <-s.wake:
		case <-s.done:
			return
		}
		for {
			s.mu.Lock()
			batch := s.pending
			s.pending, s.spare = s.spare[:0], nil
			s.mu.Unlock()
			if len(batch) == 0 {
				break
			}
			_, err := s.w.Write(batch)
			s.mu.Lock()
			if cap(batch) <= keepBatchCap {
				s.spare = batch[:0]
			}
			if err != nil {
				s.err = err
				s.pending = nil
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
		}
	}
}

// close stops the writer. A write in progress ends when the stream is closed,
// which the owner does next.
func (s *datagramSender) close() { s.closeOnce.Do(func() { close(s.done) }) }
