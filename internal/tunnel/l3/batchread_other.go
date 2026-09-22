//go:build !linux

package l3

import (
	"errors"
	"net"
)

// errNoBatch says this carrier has no recvmmsg wrapper, so the caller should
// read it one datagram at a time. It is a condition, not a failure.
var errNoBatch = errors.New("l3: this carrier cannot read in batches")

// recvmmsg is a Linux syscall. Everywhere else the carrier has no batch
// capability at all — not a stub that loops, which would be a slower way of
// doing exactly what the single-datagram path already does correctly.
func (c *udpCarrier) enableBatch() {}

// ReadBatch is never reached: without enableBatch doing anything, the carrier
// still satisfies batchReader, so the pump has to be told plainly that there is
// nothing here.
func (c *udpCarrier) ReadBatch(bufs [][]byte, sizes []int, froms []net.Addr) (int, error) {
	return 0, errNoBatch
}

// WriteBatch is never reached off Linux; see ReadBatch.
func (c *udpCarrier) WriteBatch(bufs [][]byte, to net.Addr) (int, error) {
	return 0, errNoBatch
}
