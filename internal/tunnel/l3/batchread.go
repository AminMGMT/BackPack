package l3

import "net"

// Reading several datagrams per syscall.
//
// The receive pump used to read the carrier one datagram at a time, and the
// comment there explained why it could not do otherwise: the send path batches
// because one tun.Read hands back several packets, and there is no equivalent
// on the way in without either blocking on a read that may not come or holding
// packets on a timer. Both trade latency for syscalls on the path where latency
// is the thing being protected.
//
// That reasoning is sound and it does not apply to recvmmsg, which is the whole
// point of it. recvmmsg blocks exactly as long as a single recvfrom would — until
// the first datagram — and then takes whatever else has already arrived without
// waiting for it. An idle tunnel pays nothing: one datagram arrives, one
// datagram comes back, and the call returns. A busy tunnel pays one syscall for
// a burst instead of one per packet. There is no timer and no added latency,
// which is why this is the one batching change worth making here.
//
// It is a capability, not a requirement. Only the plain UDP carrier has a
// socket recvmmsg can be pointed at; the obfuscated, QUIC, SNI and multipath
// carriers each do their own work per datagram and are left reading one at a
// time, which is what they did before.
//
// # Measured
//
// 40,000 1200-byte datagrams at a loopback carrier, 2026-09-21
// (TestBatchReceiveRate):
//
//	one by one   27,682 of 40,000 received,  59 kpps
//	recvmmsg     40,000 of 40,000 received, 214 kpps
//
// The rate is the smaller half of that result. The larger half is the first
// column: reading one at a time, the receive side could not keep up with the
// sender and the socket dropped 12,318 datagrams — nearly a third — while the
// batched path took every one. A tunnel losing packets because its own receive
// loop is behind looks exactly like a lossy path from the outside, which is the
// most expensive kind of fault to diagnose.

// batchSize is how many datagrams one recvmmsg may gather.
//
// The kernel fills as many as have arrived and returns; a larger array costs
// only the address space it occupies. Eight is where the syscall saving has
// already flattened out on a busy tunnel, and it keeps the per-pump buffer
// allocation — batchSize × (maxMTU+256) — to well under a megabyte.
const batchSize = 8

// batchWriter is the same capability in the other direction: several datagrams
// on the wire from one syscall.
//
// The send path is the side that already has a batch in hand. tun.Read returns
// as many packets as the kernel had ready, they are all sealed in the same
// loop, and they are all going to the same peer — so the gather costs nothing
// to arrange. Each one was then written with its own syscall anyway.
type batchWriter interface {
	// WriteBatch sends bufs[0:n] to the same address and reports how many left.
	// It never blocks waiting for room.
	WriteBatch(bufs [][]byte, to net.Addr) (int, error)
}

// asBatchWriter returns the carrier's batch-send capability, or nil.
func asBatchWriter(c DatagramCarrier) batchWriter {
	bw, ok := c.(batchWriter)
	if !ok {
		return nil
	}
	return bw
}

// batchReader is a carrier that can hand over several datagrams from one
// syscall. A carrier that cannot simply does not implement it, and the pump
// reads it one datagram at a time exactly as it always did.
type batchReader interface {
	// ReadBatch fills the front of bufs and reports how many datagrams it
	// wrote, with sizes[i] and froms[i] describing bufs[i]. It blocks until at
	// least one datagram arrives and never waits for a second.
	ReadBatch(bufs [][]byte, sizes []int, froms []net.Addr) (int, error)
}

// asBatchReader returns the carrier's batch capability, or nil.
func asBatchReader(c DatagramCarrier) batchReader {
	br, ok := c.(batchReader)
	if !ok {
		return nil
	}
	return br
}
