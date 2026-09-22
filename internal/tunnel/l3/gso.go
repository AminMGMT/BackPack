package l3

// Letting the kernel cut one write into many datagrams.
//
// `sendmmsg` already put a whole batch on the wire with one syscall, and
// measuring it found what the measurement in docs/performance-notes.md records:
// eight times fewer syscalls and no throughput change, because the syscall was
// not what cost. The cost is per datagram, inside the kernel — a copy into the
// socket buffer and a pass down the protocol stack for each one — and batching
// the *calls* does nothing about that.
//
// UDP segmentation offload does. `UDP_SEGMENT` hands the kernel one buffer and
// a segment size and has it cut the buffer up, so the copy and the protocol
// pass happen once for the run instead of once per datagram. Where the hardware
// supports it the segmentation happens on the card and the kernel touches the
// run once; where it does not, the kernel still only walks the stack once.
//
// Measured on this carrier, 40,000 datagrams of 1,200 bytes, three runs —
// `TestGSOSendRate` is the measurement and it is kept:
//
//	sendmmsg     8 per call   5,000 syscalls    ~255 kpps
//	UDP_SEGMENT  8 per call   5,000 syscalls    ~700 kpps    +123% … +229%
//	UDP_SEGMENT 48 per call     834 syscalls   ~1190 kpps    +320% … +398%
//
// The middle row is the one that decided it: the same batch and the same number
// of syscalls, two to three times the rate. The gain is the offload, not the
// batching, so it is worth having even at the batch this carrier reads today.
//
// # What it cannot do
//
// Every segment but the last must be the same size. That is a property of the
// mechanism, not a limitation of this implementation, and it decides when the
// path is taken: a run of full-sized packets — which is what a transfer through
// the tunnel is — qualifies, and the ragged traffic in between does not. So the
// optimisation applies where the tunnel is busiest, which is the right place
// for it, and everything else takes the `sendmmsg` path exactly as before.
//
// # Why failure has to be handled rather than prevented
//
// Support depends on the kernel, the socket family and the route, and there is
// no reliable way to ask in advance — the answer arrives as an error from a
// write that has already been attempted. So the first refusal turns the feature
// off for the life of the socket and the batch is re-sent the old way. Nothing
// is dropped, and nothing keeps paying for a probe that has already failed.

// gsoMaxSegments is the most datagrams one offloaded write may carry.
//
// The kernel's own ceiling is 64. The binding limit here is lower and arrives
// first: the whole run is one buffer as far as the send call is concerned, and
// a UDP payload stops at 65,507 bytes, so a run of full-sized packets is
// refused outright well before 64. The total is checked against that below;
// this is only the count.
const gsoMaxSegments = 64

// gsoMaxPayload is the largest buffer one write may describe: the UDP payload
// ceiling, less nothing, because the segment size is carried out of band.
const gsoMaxPayload = 65507

// gsoEligible reports whether a batch can be sent as one segmented write, and
// the segment size to declare if it can.
//
// The rules are the mechanism's: at least two datagrams (one is just a write),
// every one the same size except the last, the last no larger than the rest,
// and the whole run inside one UDP payload.
//
// It is a pure function so the rules can be tested without a socket, which
// matters more here than usual: the failure mode of getting them wrong is not
// an error, it is the kernel cutting somebody's packets in the wrong places.
func gsoEligible(bufs [][]byte) (segment int, ok bool) {
	if len(bufs) < 2 || len(bufs) > gsoMaxSegments {
		return 0, false
	}
	segment = len(bufs[0])
	if segment == 0 || segment > gsoMaxPayload {
		return 0, false
	}
	total := 0
	for i, b := range bufs {
		switch {
		case i == len(bufs)-1:
			// The last one may be short, and may not be long.
			if len(b) == 0 || len(b) > segment {
				return 0, false
			}
		case len(b) != segment:
			return 0, false
		}
		total += len(b)
	}
	if total > gsoMaxPayload {
		return 0, false
	}
	return segment, true
}
