//go:build linux

package l3

import (
	"errors"
	"net"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// errNoBatch says this socket has no recvmmsg wrapper, so the caller should
// read it one datagram at a time. It is a condition, not a failure.
var errNoBatch = errors.New("l3: this carrier cannot read in batches")

// ReadBatch gathers whatever has already arrived on the UDP socket.
//
// x/net's ReadBatch is recvmmsg on Linux. The message array is allocated once
// and reused: allocating it per call would hand back on the heap what the
// syscall saving just won.
//
// ipv4.Message and ipv6.Message are both aliases of socket.Message, so one
// array serves either family — the two wrappers differ in which socket options
// they understand, not in the shape of a message.
func (c *udpCarrier) ReadBatch(bufs [][]byte, sizes []int, froms []net.Addr) (int, error) {
	n := min(len(bufs), min(len(sizes), len(froms)))
	if n == 0 || (c.v4 == nil && c.v6 == nil) {
		return 0, errNoBatch
	}

	c.batchMu.Lock()
	defer c.batchMu.Unlock()

	if len(c.msgs) < n {
		c.msgs = make([]ipv4.Message, n)
		for i := range c.msgs {
			// One reused one-element slice per message, so a call does not
			// allocate a slice header per datagram.
			c.msgs[i].Buffers = make([][]byte, 1)
		}
	}
	msgs := c.msgs[:n]
	for i := range msgs {
		msgs[i].Buffers[0] = bufs[i]
		msgs[i].Addr = nil
		msgs[i].N = 0
	}

	var (
		got int
		err error
	)
	if c.v4 != nil {
		got, err = c.v4.ReadBatch(msgs, 0)
	} else {
		got, err = c.v6.ReadBatch([]ipv6.Message(msgs), 0)
	}
	if err != nil {
		return 0, err
	}
	for i := 0; i < got; i++ {
		sizes[i] = msgs[i].N
		froms[i] = msgs[i].Addr
	}
	return got, nil
}

// enableBatch attaches the recvmmsg wrapper to a freshly bound UDP socket.
//
// Failure is not an error: a socket with no wrapper simply has no batch
// capability and is read one datagram at a time, which is what every carrier
// did before this existed.
func (c *udpCarrier) enableBatch() {
	if c.UDPConn == nil {
		return
	}
	if la, ok := c.UDPConn.LocalAddr().(*net.UDPAddr); ok && la.IP.To4() == nil && la.IP != nil {
		c.v6 = ipv6.NewPacketConn(c.UDPConn)
		return
	}
	c.v4 = ipv4.NewPacketConn(c.UDPConn)
}

// WriteBatch puts several datagrams on the wire with one syscall.
//
// x/net's WriteBatch is sendmmsg on Linux. The message array is reused for the
// same reason the read side reuses one: allocating it per call would hand back
// on the heap what the syscall saving just won.
//
// It reports how many the kernel accepted. A short write is not an error — the
// socket buffer filled — and the caller must treat the rest as dropped, which
// is what a UDP carrier does with them anyway.
func (c *udpCarrier) WriteBatch(bufs [][]byte, to net.Addr) (int, error) {
	if len(bufs) == 0 {
		return 0, errors.New("l3: nothing to send")
	}
	if c.v4 == nil && c.v6 == nil {
		return 0, errNoBatch
	}

	c.wbatchMu.Lock()
	defer c.wbatchMu.Unlock()

	// One segmented write beats one sendmmsg by two to three times at the same
	// batch — see gso.go for the measurement. It is tried first and falls
	// through to the batch below on any refusal, having sent nothing.
	if n, err := c.writeGSO(bufs, to); err == nil {
		return n, nil
	}

	n := len(bufs)
	if len(c.wmsgs) < n {
		c.wmsgs = make([]ipv4.Message, n)
		for i := range c.wmsgs {
			c.wmsgs[i].Buffers = make([][]byte, 1)
		}
	}
	msgs := c.wmsgs[:n]
	for i := range msgs {
		msgs[i].Buffers[0] = bufs[i]
		msgs[i].Addr = to
		msgs[i].N = 0
	}

	if c.v4 != nil {
		return c.v4.WriteBatch(msgs, 0)
	}
	return c.v6.WriteBatch([]ipv6.Message(msgs), 0)
}
