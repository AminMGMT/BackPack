package network

import (
	"errors"
	"net"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Reading pck segments several at a time.
//
// The layer-3 tunnel reads its carrier in batches where it can (see batchread.go
// in the l3 package), and the batch is what lets it hand the interface a run of
// packets in one write, which the TUN device coalesces into large segments
// before the kernel's receive path sees them. A carrier that reads one frame
// per call gives it batches of one, and nothing to coalesce.
//
// recvmmsg on the AF_PACKET socket blocks exactly as long as a single read would
// — until the first frame — and then takes whatever else has already arrived.
// An idle tunnel pays nothing extra and a busy one pays one syscall per burst.

// mmsghdr is the kernel's struct mmsghdr. Go pads it to the kernel's size on
// both word sizes: 64 bytes on 64-bit, 32 on 32-bit.
type mmsghdr struct {
	Hdr unix.Msghdr
	Len uint32
}

// pckRecvBatch holds recvmmsg's arrays, allocated once and reused. Only the
// goroutine reading the carrier touches it; mu makes a second one safe anyway.
type pckRecvBatch struct {
	mu   sync.Mutex
	msgs []mmsghdr
	iovs []unix.Iovec
}

// ReadBatch reads up to len(bufs) of this tunnel's segments. bufs[i] receives a
// whole frame first and is left holding only its payload, with sizes[i] and
// froms[i] describing it. It blocks until at least one segment is this
// tunnel's, and never waits for a second.
func (c *pckConn) ReadBatch(bufs [][]byte, sizes []int, froms []net.Addr) (int, error) {
	if c.rxConn == nil {
		return 0, errPckNoBatch
	}
	// The package has a two-argument min of its own, which hides the builtin.
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

	b := &c.rxBatch
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.msgs) < n {
		b.msgs = make([]mmsghdr, n)
		b.iovs = make([]unix.Iovec, n)
	}
	msgs, iovs := b.msgs[:n], b.iovs[:n]

	for {
		for i := range n {
			if len(bufs[i]) == 0 {
				return 0, errPckNoBatch
			}
			iovs[i].Base = &bufs[i][0]
			iovs[i].SetLen(len(bufs[i]))
			msgs[i] = mmsghdr{}
			msgs[i].Hdr.Iov = &iovs[i]
			msgs[i].Hdr.SetIovlen(1)
		}

		var got int
		var serr error
		err := c.rxConn.Read(func(fd uintptr) bool {
			r, _, e := unix.Syscall6(unix.SYS_RECVMMSG, fd,
				uintptr(unsafe.Pointer(&msgs[0])), uintptr(n), unix.MSG_DONTWAIT, 0, 0)
			if e == unix.EAGAIN {
				return false // wait in the poller, as a blocking read would
			}
			if e != 0 {
				serr = e
				return true
			}
			got = int(r)
			return true
		})
		if err != nil {
			return 0, err // closed, or a read deadline
		}
		if serr != nil {
			if errors.Is(serr, unix.EINTR) {
				continue
			}
			return 0, serr
		}

		// Keep this tunnel's segments, packed to the front. A slot is only
		// written once every frame at or before it has been read, so moving a
		// payload down never overwrites a frame still to be looked at.
		out := 0
		for i := 0; i < got; i++ {
			payload, addr, ok := c.accept(bufs[i][:msgs[i].Len])
			if !ok {
				continue
			}
			sizes[out] = copy(bufs[out], payload)
			froms[out] = addr
			out++
		}
		if out > 0 {
			return out, nil
		}
		// Nothing in this burst was ours; wait for the next.
	}
}

// Sending several segments per syscall.
//
// The layer-3 tunnel's send side reads the interface in batches — with
// segmentation offload one read can return a whole run of a flow — and seals
// them in one loop, all for the same peer. Each then went out with its own
// sendto, which was the largest single cost of a loaded pck tunnel: 30% of its
// CPU in a profile. sendmmsg puts the batch out in one call.

// pckSendBatch holds sendmmsg's arrays and the frames it sends, reused.
type pckSendBatch struct {
	mu     sync.Mutex
	msgs   []mmsghdr
	iovs   []unix.Iovec
	frames [][]byte
	name   unix.RawSockaddrLinklayer
}

// WriteBatch sends bufs to one peer as consecutive segments of its flow and
// reports how many went out. Frames are injected at the link layer; a carrier
// without that path declines, and the caller sends one at a time.
func (c *pckConn) WriteBatch(bufs [][]byte, to net.Addr) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	if c.txConn == nil || c.txAddr == nil {
		return 0, errPckNoBatch
	}
	dst, ok := toUDPAddr(to)
	if !ok {
		return 0, net.InvalidAddrError("pck: unusable destination address")
	}
	n := len(bufs)
	if n == 0 {
		return 0, nil
	}
	peer := c.peerFor(dst)

	b := &c.txBatch
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.msgs) < n {
		b.msgs = make([]mmsghdr, n)
		b.iovs = make([]unix.Iovec, n)
		frames := make([][]byte, n)
		copy(frames, b.frames)
		b.frames = frames
		b.name = unix.RawSockaddrLinklayer{
			Family:   unix.AF_PACKET,
			Protocol: htons(unix.ETH_P_IP),
			Ifindex:  int32(c.txAddr.Ifindex),
			Halen:    c.txAddr.Halen,
			Addr:     c.txAddr.Addr,
		}
	}

	// The sequence numbers for the whole batch in one critical section,
	// advancing by each payload exactly as consecutive sends would.
	c.mu.Lock()
	seq0, ack, tsEcr := peer.seq, peer.ack, peer.lastTS
	for _, p := range bufs {
		peer.seq += uint32(len(p))
	}
	c.mu.Unlock()

	// One clock read for the batch: the timestamp is in milliseconds and a
	// batch goes out within one.
	ts := c.timestamp()
	seq := seq0
	dstIP := dst.IP.To4()
	for i, p := range bufs {
		need := pckFrameLen(len(p))
		if cap(b.frames[i]) < need {
			b.frames[i] = make([]byte, need)
		}
		frame := assemblePckFrame(b.frames[i][:cap(b.frames[i])], pckFrameParams{
			SrcMAC: c.egress.SrcMAC, DstMAC: c.egress.NextHop,
			SrcIP: c.egress.LocalIP, DstIP: dstIP,
			SrcPort: c.local, DstPort: uint16(dst.Port),
			Seq: seq, Ack: ack,
			Flags: c.flags[int(c.flagRot.Add(1)-1)%len(c.flags)],
			TSVal: ts, TSEcr: tsEcr,
			ID:      uint16(c.ipID.Add(1)),
			Payload: p,
		})
		seq += uint32(len(p))

		b.iovs[i].Base = &frame[0]
		b.iovs[i].SetLen(len(frame))
		b.msgs[i] = mmsghdr{}
		b.msgs[i].Hdr.Name = (*byte)(unsafe.Pointer(&b.name))
		b.msgs[i].Hdr.Namelen = unix.SizeofSockaddrLinklayer
		b.msgs[i].Hdr.Iov = &b.iovs[i]
		b.msgs[i].Hdr.SetIovlen(1)
	}

	sent := 0
	for sent < n {
		var got int
		var serr error
		err := c.txConn.Write(func(fd uintptr) bool {
			r, _, e := unix.Syscall6(unix.SYS_SENDMMSG, fd,
				uintptr(unsafe.Pointer(&b.msgs[sent])), uintptr(n-sent), unix.MSG_DONTWAIT, 0, 0)
			if e == unix.EAGAIN {
				return false // the device queue is full: wait, as a single send does
			}
			if e != 0 {
				serr = e
				return true
			}
			got = int(r)
			return true
		})
		if err != nil {
			return sent, err
		}
		if serr != nil {
			if errors.Is(serr, unix.EINTR) {
				continue
			}
			return sent, serr
		}
		if got == 0 {
			break
		}
		sent += got
	}
	return sent, nil
}
