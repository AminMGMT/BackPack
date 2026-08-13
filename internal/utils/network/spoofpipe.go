package network

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
)

// The WireGuard-pipe mode carries a raw UDP flow over the spoof channel instead
// of a KCP tunnel. WireGuard already brings its own encryption and its own
// handling of a lossy link, so stacking KCP under it would only double the
// reliability layer and add latency; the pipe therefore strips all of that and
// relays datagrams directly, forging the source exactly as the tunnel does.
//
// One WireGuard socket multiplexes all of its peers, so a single pipe carries a
// whole WireGuard instance — the whole-device L3 VPN the tunnel's SOCKS proxy
// cannot give.

// SizePipeUDP enlarges the send and receive buffers of the WireGuard-side UDP
// socket in the pipe, mirroring the carrier's own socket buffers so neither end
// of the relay is the one that drops a burst. sockBuf <= 0 uses the carrier
// default. Best effort: the kernel clamps to its limits and any refusal is
// ignored, since the pipe still works with the default buffer.
func SizePipeUDP(pc net.PacketConn, sockBuf int) {
	if sockBuf <= 0 {
		sockBuf = defaultSpoofSockBuf
	}
	applySockBuf(pc, sockBuf)
}

// NewSpoofPacketConn opens the spoof carrier as a bare net.PacketConn, for the
// pipe (and any caller that wants the forged-source datagram transport without
// KCP on top). realPeer is the peer's real address: the server's for a client,
// the client's for a server.
func NewSpoofPacketConn(server bool, token string, c SpoofCarrier, realPeer net.IP) (net.PacketConn, error) {
	if server {
		return newSpoofServerConn(c.spoofOpts(token, realPeer))
	}
	return newSpoofClientConn(c.spoofOpts(token, realPeer))
}

// SpoofPipeRelay shuttles datagrams between the spoof carrier and a local UDP
// socket until the context is cancelled or either side fails, and returns the
// reason. When learnPeer is set (the client, whose udp is a listener that
// WireGuard sends to), replies are addressed back to the last source seen;
// otherwise (the server, whose udp is dialled to the real WireGuard endpoint)
// the connected socket's Write is used.
func SpoofPipeRelay(ctx context.Context, spoof net.PacketConn, udp *net.UDPConn, learnPeer bool) error {
	errc := make(chan error, 2)
	var peer atomic.Pointer[net.UDPAddr]

	// udp (WireGuard) -> spoof (to the tunnel peer)
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, addr, err := udp.ReadFromUDP(buf)
			if err != nil {
				errc <- err
				return
			}
			if learnPeer && addr != nil {
				peer.Store(addr)
			}
			if _, err := spoof.WriteTo(buf[:n], nil); err != nil {
				errc <- err
				return
			}
		}
	}()

	// spoof (from the tunnel peer) -> udp (WireGuard), decoupled into a reader and
	// a writer joined by a bounded queue, as the reference transports do. The
	// reader's only job is to drain the kernel receive buffer the instant a packet
	// lands, so a momentary stall writing to WireGuard never backs up into the
	// socket and drops the tunnel's traffic. Buffers come from a pool, so the
	// decoupling costs no per-packet allocation; when the queue is full the oldest
	// datagram is dropped (this is a lossy L3 pipe — WireGuard recovers), never
	// the newest, and the buffer is always returned to the pool.
	const queueDepth = 1024
	q := make(chan []byte, queueDepth)
	var pool = sync.Pool{New: func() any { b := make([]byte, 64*1024); return &b }}
	get := func() *[]byte { return pool.Get().(*[]byte) }
	put := func(b []byte) { bb := b[:cap(b)]; pool.Put(&bb) }

	// reader: spoof -> queue
	go func() {
		for {
			bp := get()
			n, _, err := spoof.ReadFrom(*bp)
			if err != nil {
				errc <- err
				return
			}
			pkt := (*bp)[:n]
			select {
			case q <- pkt:
			default:
				// Queue full: drop the oldest to make room for this one, so a
				// transient writer stall sheds load instead of blocking the reader.
				select {
				case old := <-q:
					put(old)
				default:
				}
				select {
				case q <- pkt:
				default:
					put(pkt)
				}
			}
		}
	}()

	// writer: queue -> udp (WireGuard)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case pkt, ok := <-q:
				if !ok {
					return
				}
				if learnPeer {
					if a := peer.Load(); a != nil {
						_, _ = udp.WriteToUDP(pkt, a)
					}
				} else {
					_, _ = udp.Write(pkt)
				}
				put(pkt)
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errc:
		return err
	}
}
