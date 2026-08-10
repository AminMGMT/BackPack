package transport

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/utils/network"
	"github.com/sirupsen/logrus"
)

// Forwarded UDP, for every transport.
//
// A forwarded port used to mean a TCP listener and nothing else. Only the plain
// TCP transport could carry datagrams, only behind accept_udp, and it did it
// with its own private copy of the logic — so a tunnel on tcpmux, ws, wss,
// wsmux, kcp or quic simply never bound the UDP port. Nothing said so. The
// datagrams were refused by the kernel, and the operator saw a forwarded port
// where TCP worked and UDP did not, which is what Xray, 3x-ui and Shadowsocks
// users kept running into.
//
// The fix is this file: one UDP forwarder, and each flow presented as an
// ordinary net.Conn. Every transport already knows how to pair a net.Conn with
// a tunnel connection and pipe the two together — that is exactly what it does
// for TCP — so handing it a datagram flow wearing the same shape means
// forwarded UDP works everywhere without a second implementation of the
// pairing, the limits, the metrics or the teardown.
//
// The wire format is network.WriteDatagram/ReadDatagram: two bytes of length,
// then the payload. udpFlow.Read produces it and udpFlow.Write consumes it, so
// the bytes between the two ends are frames however the transport chunks them.

// udpIdleTimeout is how long a flow with no traffic in either direction is
// kept. UDP has no close, so the only way a flow ends is by going quiet: this
// is the NAT-style mapping lifetime, and it has to outlast the gaps a real
// protocol leaves. 60s covers a DNS exchange many times over and sits inside
// the 30-120s most home routers use for their own UDP mappings, so a peer that
// keeps its side alive keeps ours alive too.
const udpIdleTimeout = 60 * time.Second

// udpFlowQueue is how many datagrams may wait for the tunnel connection that
// will carry them. A flow's first datagrams arrive before the transport has
// paired it with a stream, and dropping those loses the handshake that starts
// every session — which reads as "UDP does not work" rather than as one lost
// packet. Deep enough to absorb that pause, bounded so a peer that floods a
// stalled flow cannot grow the process without limit.
const udpFlowQueue = 256

// udpForwarder owns one UDP socket on a forwarded port and the flows on it.
type udpForwarder struct {
	conn   *net.UDPConn
	logger *logrus.Logger
	// target is the address on the far side, already marked as UDP.
	target string

	mu     sync.Mutex
	flows  map[string]*udpFlow
	closed bool
}

// newUDPForwarder binds the UDP side of a forwarded port.
//
// A failure here is reported and nothing else: it must not be fatal. The port
// may well be taken on UDP by something unrelated while TCP is free, and
// killing the whole server — every other tunnel port with it — over one
// unavailable datagram socket is a far worse outcome than the one forwarded
// port not carrying UDP.
func newUDPForwarder(logger *logrus.Logger, localAddr, remoteAddr string) (*udpForwarder, error) {
	addr, err := net.ResolveUDPAddr("udp", localAddr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}
	return &udpForwarder{
		conn:   conn,
		logger: logger,
		target: network.MarkUDP(remoteAddr),
		flows:  map[string]*udpFlow{},
	}, nil
}

// startUDPForward binds the UDP side of a forwarded port and reads from it
// until the context ends, handing each new source address to admit as a
// net.Conn. admit returns false when the transport cannot take the flow right
// now — its channel is full, or the tunnel's connection limit is reached — and
// the flow is then dropped whole, so the next datagram from that address starts
// a fresh attempt.
//
// That last part is the difference between a busy moment and a dead flow. The
// implementation this replaces recorded the new source address before offering
// it, and left the record behind when the offer failed; every later datagram
// from that address was filed against a flow no goroutine was reading, so one
// full channel silenced that peer until the service was restarted.
func startUDPForward(ctx context.Context, logger *logrus.Logger, localAddr, remoteAddr string,
	admit func(conn net.Conn, target string) bool) {

	fw, err := newUDPForwarder(logger, localAddr, remoteAddr)
	if err != nil {
		logger.Warnf("forwarded port %s carries TCP only — its UDP side could not be opened: %v", localAddr, err)
		return
	}
	logger.Infof("UDP listener started successfully, listening on address: %s", fw.conn.LocalAddr())

	go fw.reap(ctx)
	go func() {
		<-ctx.Done()
		fw.close()
	}()
	fw.run(admit)
}

// run reads datagrams and files each one under its source address.
func (fw *udpForwarder) run(admit func(conn net.Conn, target string) bool) {
	buf := make([]byte, network.MaxDatagram)
	for {
		n, peer, err := fw.conn.ReadFromUDP(buf)
		if err != nil {
			fw.mu.Lock()
			closed := fw.closed
			fw.mu.Unlock()
			if closed {
				return
			}
			fw.logger.Errorf("failed to read from UDP listener %s: %v", fw.conn.LocalAddr(), err)
			continue
		}

		key := peer.String()
		fw.mu.Lock()
		flow, known := fw.flows[key]
		if !known && !fw.closed {
			flow = newUDPFlow(fw, peer, key)
			fw.flows[key] = flow
		}
		fw.mu.Unlock()
		if flow == nil {
			return // the forwarder is shutting down
		}

		// The first datagram is queued before the flow is offered, so the
		// session's opening packet is the first thing the tunnel carries rather
		// than something that arrived while nobody was listening yet.
		flow.deliver(buf[:n])

		if !known {
			if !admit(flow, fw.target) {
				fw.logger.Warnf("could not start a UDP flow from %s — dropping it; the next packet will try again", key)
				flow.Close()
			}
		}
	}
}

// reap closes flows that have gone quiet in both directions.
func (fw *udpForwarder) reap(ctx context.Context) {
	ticker := time.NewTicker(udpIdleTimeout / 4)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-udpIdleTimeout).UnixNano()
			fw.mu.Lock()
			var stale []*udpFlow
			for _, f := range fw.flows {
				if f.lastSeen.Load() < cutoff {
					stale = append(stale, f)
				}
			}
			fw.mu.Unlock()
			for _, f := range stale {
				fw.logger.Debugf("closing idle UDP flow from %s", f.peer)
				f.Close()
			}
		}
	}
}

func (fw *udpForwarder) close() {
	fw.mu.Lock()
	fw.closed = true
	flows := make([]*udpFlow, 0, len(fw.flows))
	for _, f := range fw.flows {
		flows = append(flows, f)
	}
	fw.mu.Unlock()
	for _, f := range flows {
		f.Close()
	}
	fw.conn.Close()
}

func (fw *udpForwarder) forget(key string, f *udpFlow) {
	fw.mu.Lock()
	// Only if it is still the same flow: a peer that came back after a reap
	// has a newer one under this key, and removing that would strand it.
	if fw.flows[key] == f {
		delete(fw.flows, key)
	}
	fw.mu.Unlock()
}

// udpFlow is everything arriving from one source address on a forwarded UDP
// port, dressed as a net.Conn so a transport can pair it with a tunnel
// connection exactly as it pairs an accepted TCP connection.
//
// Read yields those datagrams framed; Write takes framed datagrams from the
// tunnel and sends them back to the source. Both are called by the generic
// relay, which knows nothing about datagrams.
type udpFlow struct {
	fwd  *udpForwarder
	peer *net.UDPAddr
	key  string

	in   chan []byte
	done chan struct{}
	once sync.Once

	lastSeen atomic.Int64

	// pending holds the tail of a frame a short Read could not finish.
	pending []byte
	// The reassembly state for Write: the tunnel hands over arbitrary chunks,
	// so a frame may arrive across several calls, or several frames in one.
	hdr  [2]byte
	hdrN int
	body []byte
	want int
}

func newUDPFlow(fw *udpForwarder, peer *net.UDPAddr, key string) *udpFlow {
	f := &udpFlow{
		fwd:  fw,
		peer: peer,
		key:  key,
		in:   make(chan []byte, udpFlowQueue),
		done: make(chan struct{}),
	}
	f.touch()
	return f
}

func (f *udpFlow) touch() { f.lastSeen.Store(time.Now().UnixNano()) }

// deliver queues a datagram for the tunnel. A full queue means the tunnel is
// not keeping up; dropping is what a congested UDP path does anyway, and it is
// better than blocking the one goroutine that serves every flow on this port.
func (f *udpFlow) deliver(payload []byte) {
	select {
	case f.in <- append([]byte(nil), payload...):
		f.touch()
	case <-f.done:
	default:
		f.fwd.logger.Debugf("UDP queue full for %s, dropping a datagram", f.key)
	}
}

func (f *udpFlow) Read(p []byte) (int, error) {
	for len(f.pending) == 0 {
		select {
		case payload := <-f.in:
			frame := make([]byte, 2+len(payload))
			frame[0] = byte(len(payload) >> 8)
			frame[1] = byte(len(payload))
			copy(frame[2:], payload)
			f.pending = frame
		case <-f.done:
			// Drain what is already queued before reporting the end, so a flow
			// closed while its last datagrams were in flight still delivers
			// them rather than dropping them on the floor.
			select {
			case payload := <-f.in:
				frame := make([]byte, 2+len(payload))
				frame[0] = byte(len(payload) >> 8)
				frame[1] = byte(len(payload))
				copy(frame[2:], payload)
				f.pending = frame
			default:
				return 0, io.EOF
			}
		}
	}
	n := copy(p, f.pending)
	f.pending = f.pending[n:]
	f.touch()
	return n, nil
}

// Write reassembles framed datagrams out of whatever the tunnel hands over and
// sends each completed one back to the source address.
func (f *udpFlow) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		select {
		case <-f.done:
			return total - len(p), net.ErrClosed
		default:
		}

		if f.hdrN < 2 {
			n := copy(f.hdr[f.hdrN:], p)
			f.hdrN += n
			p = p[n:]
			if f.hdrN < 2 {
				break
			}
			f.want = int(f.hdr[0])<<8 | int(f.hdr[1])
			f.body = make([]byte, 0, f.want)
			if f.want == 0 {
				if err := f.send(nil); err != nil {
					return total - len(p), err
				}
				f.hdrN = 0
			}
			continue
		}

		need := f.want - len(f.body)
		if need > len(p) {
			need = len(p)
		}
		f.body = append(f.body, p[:need]...)
		p = p[need:]
		if len(f.body) == f.want {
			if err := f.send(f.body); err != nil {
				return total - len(p), err
			}
			f.hdrN, f.want, f.body = 0, 0, nil
		}
	}
	return total, nil
}

// send puts one datagram back on the wire, from the forwarded port itself so
// the source sees the reply coming from the address it sent to.
func (f *udpFlow) send(payload []byte) error {
	if _, err := f.fwd.conn.WriteToUDP(payload, f.peer); err != nil {
		return err
	}
	f.touch()
	return nil
}

func (f *udpFlow) Close() error {
	f.once.Do(func() {
		close(f.done)
		f.fwd.forget(f.key, f)
	})
	return nil
}

// LocalAddr is the forwarded port, which is what the metrics and the sniffer
// record a flow against.
func (f *udpFlow) LocalAddr() net.Addr  { return f.fwd.conn.LocalAddr() }
func (f *udpFlow) RemoteAddr() net.Addr { return f.peer }

// A datagram flow has no deadlines of its own: it is bounded by the idle reaper
// above, and the relay never sets one. Accepting them silently keeps it a
// net.Conn without pretending to honour something it does not implement.
func (f *udpFlow) SetDeadline(time.Time) error      { return nil }
func (f *udpFlow) SetReadDeadline(time.Time) error  { return nil }
func (f *udpFlow) SetWriteDeadline(time.Time) error { return nil }

// udpAdmitter builds the callback startUDPForward hands a new flow to.
//
// It is the same three steps every transport already takes for an accepted TCP
// connection — take a slot from the connection limit, put it on the local
// channel, nudge the pool for somewhere to carry it — so a datagram flow is
// paired, counted and torn down by the code that does it for everything else.
// The slot is released where every other one is: by the handler that finishes
// the transfer, or by the loop that gives up on a connection nobody claimed.
func udpAdmitter(local chan LocalTCPConn, reqNewConn chan struct{}, limits *limiter) func(net.Conn, string) bool {
	return func(conn net.Conn, target string) bool {
		if !limits.acquire() {
			return false
		}
		select {
		case local <- LocalTCPConn{conn: limits.wrap(conn), remoteAddr: target, timeCreated: time.Now().UnixMilli()}:
			select {
			case reqNewConn <- struct{}{}:
			default:
			}
			return true
		default:
			limits.release()
			return false
		}
	}
}

// localForwardPort is the forwarded port a local connection arrived on.
//
// It used to be read with a bare type assertion to *net.TCPAddr, which was true
// of every local connection back when they were all TCP. A UDP flow is a
// net.Conn too, and that assertion would panic on the first datagram — so the
// port is asked for rather than assumed.
func localForwardPort(conn net.Conn) int {
	switch a := conn.LocalAddr().(type) {
	case *net.TCPAddr:
		return a.Port
	case *net.UDPAddr:
		return a.Port
	default:
		if _, port, err := net.SplitHostPort(conn.LocalAddr().String()); err == nil {
			if p, err := net.LookupPort("tcp", port); err == nil {
				return p
			}
		}
		return 0
	}
}

// isUDPFlow reports whether a local connection is a forwarded datagram flow.
//
// The PROXY protocol header is the reason this is needed: it is written into
// the tunnel ahead of the payload, and on a datagram flow those bytes would be
// read as the first frame's length and body. The real client address is already
// lost to a UDP backend anyway, so the header is skipped rather than corrupting
// the stream.
func isUDPFlow(conn net.Conn) bool {
	_, ok := conn.(*udpFlow)
	return ok
}
