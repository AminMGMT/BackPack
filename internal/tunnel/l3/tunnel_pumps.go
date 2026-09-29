package l3

import (
	"context"
	"errors"
	"net"
	"time"
)

// The two pumps: interface to carrier (sealing, batching, GSO) and carrier to
// interface (opening, routing each datagram to its half of the protocol).

// pumpFromTUN reads packets the kernel routed into the interface, seals them
// and sends them to the peer.
func (t *Tunnel) pumpFromTUN(ctx context.Context) {
	batch := t.tun.BatchSize()
	if batch < 1 {
		batch = 1
	}
	bufs := make([][]byte, batch)
	for i := range bufs {
		bufs[i] = make([]byte, tunReadBuf)
	}
	sizes := make([]int, batch)

	frame := make([]byte, 0, t.cfg.MTU+t.encap.Overhead())

	// Sealed packets, one slot per slot the TUN read filled.
	//
	// Separate buffers rather than one reused for every packet, because a batch
	// send has to hold all of them at once — the single `out` buffer this used
	// worked only because each packet was written before the next was sealed.
	sealedSize := t.cfg.MTU + t.encap.Overhead() + dataOverhead
	sealed := make([][]byte, batch)
	for i := range sealed {
		sealed[i] = make([]byte, 0, sealedSize)
	}
	// The slice handed to the carrier: the first k sealed packets of this
	// round, re-sliced rather than rebuilt.
	ready := make([][]byte, batch)
	// The inner size of each one. The counters have always meant the bytes the
	// tunnel *carried*, not the bytes it put on the wire — reporting the sealed
	// size would make every tunnel look like it was moving more than it was,
	// by exactly its own overhead.
	payloads := make([]int, batch)

	// sendmmsg, when the carrier has it. Only the plain UDP carrier does; the
	// rest keep writing one datagram per syscall exactly as before. See
	// batchread.go.
	writer := asBatchWriter(t.carrier)

	for {
		count, err := t.tun.Read(bufs, sizes)
		if err != nil {
			if ctx.Err() == nil {
				t.log.Errorf("l3: reading from %s: %v", t.cfg.Iface, err)
			}
			return
		}

		// Resolved once per batch rather than once per packet. Both take the
		// state lock, and at a few thousand packets a second it was being taken
		// twice as often as anything in it changed.
		sess := t.sendSession()
		peer := t.peerAddr()

		// Seal everything this round first, then send it.
		//
		// The two used to be interleaved — seal one, write one — which is why
		// a single sealing buffer sufficed. Separating them is what lets the
		// whole round leave in one syscall, and it costs nothing when the
		// carrier cannot batch: the send loop below is the old one.
		k := 0
		var payload int
		for i := 0; i < count; i++ {
			n := sizes[i]
			if n == 0 {
				continue
			}
			if sess == nil || peer == nil {
				// Nothing to send under yet. Dropping is correct: the layer
				// above owns retransmission, and queueing here would only
				// deliver a burst of stale packets once the tunnel came up.
				t.stats.dropped.Add(1)
				continue
			}

			wrapped, err := t.encap.Wrap(frame[:0], bufs[i][:n])
			if err != nil {
				t.stats.dropped.Add(1)
				t.log.Debugf("l3: not forwarding a packet off %s: %v", t.cfg.Iface, err)
				continue
			}
			out, err := sess.seal(sealed[k][:0], wrapped)
			if err != nil {
				t.stats.dropped.Add(1)
				t.log.Warnf("l3: sealing a packet: %v", err)
				continue
			}
			sealed[k] = out
			ready[k] = out
			payloads[k] = n
			payload += n
			k++
		}
		if k == 0 {
			continue
		}
		t.noteSent()

		if !t.send(ctx, writer, ready[:k], payloads[:k], peer, payload) {
			return
		}
	}
}

// send puts a round of sealed packets on the wire, in one syscall where the
// carrier allows it.
//
// It returns false only when the run is over, so the pump can stop. Everything
// else — a short write, a refused datagram — is a drop, which is what a UDP
// carrier does with them in any case.
func (t *Tunnel) send(ctx context.Context, writer batchWriter, ready [][]byte,
	payloads []int, peer net.Addr, payload int) bool {

	if writer != nil && len(ready) > 1 {
		sent, err := writer.WriteBatch(ready, peer)
		if err == nil {
			t.account(ready, sent, payload)
			return true
		}
		if !errors.Is(err, errNoBatch) {
			if ctx.Err() != nil {
				return false
			}
			t.stats.dropped.Add(uint64(len(ready)))
			t.log.Debugf("l3: sending a batch to %s: %v", peer, err)
			return true
		}
		// The carrier declined to batch after all; fall through and write them
		// one at a time rather than dropping a round over an optimisation.
	}

	for i, p := range ready {
		if _, err := t.carrier.WriteTo(p, peer); err != nil {
			if ctx.Err() != nil {
				return false
			}
			t.stats.dropped.Add(1)
			t.log.Debugf("l3: sending to %s: %v", peer, err)
			continue
		}
		// Bytes before packets. See Stats for the pair of orderings this is
		// half of. The inner size, not len(p): see payloads above.
		t.stats.bytesOut.Add(uint64(payloads[i]))
		t.stats.packetsOut.Add(1)
	}
	return true
}

// account records a batch that went out.
//
// payload is the inner bytes the round carried, which is what the counter has
// always meant — not the sealed size, which includes the tunnel's own overhead
// and would make a tunnel look like it was carrying more than it was.
func (t *Tunnel) account(ready [][]byte, sent, payload int) {
	if sent < len(ready) {
		// The socket buffer filled. The rest are gone, which is what happens to
		// a UDP datagram there is no room for either way.
		t.stats.dropped.Add(uint64(len(ready) - sent))
	}
	if sent <= 0 {
		return
	}
	// Apportioned, because a short write does not say which ones left. Over a
	// round of packets that are all about the same size this is exact enough
	// for a throughput figure, and the packet count is not approximated at all.
	t.stats.bytesOut.Add(uint64(payload * sent / len(ready)))
	t.stats.packetsOut.Add(uint64(sent))
}

// pumpFromCarrier reads datagrams off the carrier and routes them by kind.
func (t *Tunnel) pumpFromCarrier(ctx context.Context) {
	// The receive batch: one buffer per datagram, reused for the life of the
	// pump. A carrier with no batch capability uses only the first.
	batch := 1
	reader := asBatchReader(t.carrier)
	if reader != nil {
		batch = batchSize
	}
	bufs := make([][]byte, batch)
	for i := range bufs {
		bufs[i] = make([]byte, maxMTU+256)
	}
	// One plaintext buffer per slot as well, and the packets of a whole batch
	// go to the interface in one write.
	//
	// They used to go one at a time, which was one syscall per packet and —
	// the larger cost — one trip up the kernel's receive path per packet. The
	// device takes several at once, and with segmentation offload on, the
	// library coalesces consecutive segments of one TCP flow into a single
	// large one before the kernel sees them (GRO). A batch is whatever the
	// carrier read had in hand, so nothing waits for it to fill.
	plains := make([][]byte, batch)
	for i := range plains {
		plains[i] = make([]byte, 0, maxMTU+256)
	}
	pending := make([][]byte, 0, batch)
	sizes := make([]int, batch)
	froms := make([]net.Addr, batch)
	ticker := time.NewTicker(previousGrace / 2)
	defer ticker.Stop()

	// Retiring old sessions is timer work with nothing else to do it, and the
	// receive pump is the one goroutine guaranteed to exist on both sides.
	//
	// It is tied to this pump's own return as well as to the context, so it
	// cannot outlive the generation that started it. Watching the context
	// alone leaked one of these on every restart: the ticker was stopped on
	// the way out, leaving the goroutine parked on a channel that would never
	// fire again until the process ended.
	done := make(chan struct{})
	defer close(done)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case now := <-ticker.C:
				t.retireSessions(now)
			}
		}
	}()

	for {
		got, err := t.receive(reader, bufs, sizes, froms)
		if err != nil {
			if ctx.Err() == nil {
				t.log.Errorf("l3: reading from the carrier: %v", err)
			}
			return
		}
		pending = pending[:0]
		for i := 0; i < got; i++ {
			var inner []byte
			plains[i], inner = t.route(plains[i], bufs[i][:sizes[i]], froms[i])
			if inner != nil {
				pending = append(pending, inner)
			}
		}
		t.writeToDevice(pending)
	}
}

// writeToDevice hands a batch of authenticated inner packets to the interface.
// They were counted as received when they were opened; any the device refuses
// are counted again as drops.
func (t *Tunnel) writeToDevice(pending [][]byte) {
	if len(pending) == 0 {
		return
	}
	n, err := t.tun.Write(pending)
	if err != nil {
		t.stats.dropped.Add(uint64(len(pending)))
		t.log.Debugf("l3: writing to %s: %v", t.cfg.Iface, err)
		return
	}
	if n > 0 && n < len(pending) {
		// More than the staging buffer holds in one call: the rest go now.
		t.writeToDevice(pending[n:])
	}
}

// receive takes the next datagram, or the next several. It is the only place
// that knows the carrier might be batchable, so the loop above reads the same
// either way.
//
// A batch read that fails falls through to the single read rather than killing
// the pump: recvmmsg is an optimisation, and losing it must cost throughput
// rather than the tunnel. The one exception is a real socket error, which the
// single read will hit again and report properly.
func (t *Tunnel) receive(reader batchReader, bufs [][]byte, sizes []int, froms []net.Addr) (int, error) {
	if reader != nil {
		n, err := reader.ReadBatch(bufs, sizes, froms)
		if err == nil {
			return n, nil
		}
		if !errors.Is(err, errNoBatch) {
			return 0, err
		}
	}
	n, from, err := t.carrier.ReadFrom(bufs[0])
	if err != nil {
		return 0, err
	}
	sizes[0], froms[0] = n, from
	return 1, nil
}

// route parses one datagram off the carrier and hands it to whichever half of
// the protocol owns it.
//
// A data packet is not written here: its inner packet comes back, to go to the
// interface with the rest of the batch. It points into plain, so plain must
// not be reused until it has been written.
func (t *Tunnel) route(plain []byte, datagram []byte, from net.Addr) ([]byte, []byte) {
	h, body, err := parseHeader(datagram)
	if err != nil {
		// A stray datagram on an open port: a scanner, a stale peer, or
		// noise. Not worth a log line above debug.
		t.stats.dropped.Add(1)
		return plain, nil
	}

	switch h.kind {
	case typeInit:
		t.handleInit(h, body, from)
	case typeResp:
		t.handleResp(h, body)
	case typeData:
		return t.handleData(plain, h, body, from)
	case typeProbe, typeProbeAck:
		return t.handleProbeMessage(plain, h, body, from), nil
	}
	return plain, nil
}
