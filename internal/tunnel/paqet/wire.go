package paqet

// The socket layer is derived from paqet's internal/socket (MIT); see doc.go.
// Changed: one capture and one injector shared by many sockets, so a client
// with several KCP sessions holds one pair of pcap handles, not one pair each.

import (
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Capture yields the inbound TCP segments of the tunnel's ports.
type Capture interface {
	// Next returns the payload of the next segment, where it came from and
	// which local port it was addressed to, and the link-layer address it
	// arrived from (nil on a link with none). The payload may alias an internal
	// buffer and is valid only until the next call. errNoFrame means "nothing
	// usable yet, ask again" (a timeout, or a segment with no payload).
	Next() (payload []byte, src *net.UDPAddr, dstPort uint16, srcMAC net.HardwareAddr, err error)
	Close() error
}

// Injector puts a payload on the wire as a TCP segment from srcPort to dst.
type Injector interface {
	Inject(srcPort uint16, payload []byte, dst *net.UDPAddr) error
	Close() error
}

// NextHopLearner is an Injector that can be told where a peer is reachable at
// the link layer. The listening side has no peer to look a gateway up for, so on
// a host where the lookup finds none it answers each client to the address its
// segments arrived from, which is the next hop back toward it.
type NextHopLearner interface {
	// WantsNextHops is true while the injector has no fixed next hop.
	WantsNextHops() bool
	LearnNextHop(ip net.IP, mac net.HardwareAddr)
}

// errNoFrame is Capture.Next's "try again".
var errNoFrame = errors.New("paqet: no frame")

// rxQueueLen is how many segments one socket may hold before the wire starts
// dropping for it. 2048 is two KCP windows of Balance, and the bound is what
// keeps one stalled reader from pinning the pool.
const rxQueueLen = 2048

// frameBufLen holds any segment payload: MTU is at most 1450.
const frameBufLen = 2048

var framePool = sync.Pool{New: func() any { b := make([]byte, frameBufLen); return &b }}

type rxFrame struct {
	buf *[]byte
	n   int
	src *net.UDPAddr
}

// WireStats are the wire's counters.
type WireStats struct {
	PacketsIn, PacketsOut uint64
	BytesIn, BytesOut     uint64
	// RxDrops counts segments a socket had no room for; Foreign counts
	// segments recognised as the previous PCK's handshake.
	RxDrops, Foreign uint64
	// TxErrors counts segments that could not be sent (a full transmit queue,
	// mostly); KCP retransmits them.
	TxErrors uint64
	// Sessions counts sessions that completed a hello; the engine fills it.
	Sessions uint64
}

// Wire is the capture and the injector of one engine generation, and the table
// that hands each captured segment to the socket that owns its destination
// port.
type Wire struct {
	cap Capture
	inj Injector
	// learner is inj, when it takes next hops from arriving segments.
	learner NextHopLearner
	// local is the address sockets report as theirs.
	local net.IP

	mu    sync.RWMutex
	socks map[uint16]*Socket

	closeOnce sync.Once
	done      chan struct{}
	readerEnd chan struct{}

	// onForeign is called, rate-limited by the caller, for a segment that is
	// the previous PCK's handshake.
	onForeign atomic.Pointer[func(src net.Addr)]

	pktsIn, pktsOut, bytesIn, bytesOut atomic.Uint64
	rxDrops, foreign                   atomic.Uint64

	// txErrs counts segments the injector could not send, and lastTxErr is the
	// most recent reason. See Socket.WriteTo.
	txErrs    atomic.Uint64
	lastTxErr atomic.Pointer[error]
}

// NewWire starts reading cap. local is the address reported by Socket.LocalAddr.
func NewWire(cap Capture, inj Injector, local net.IP) *Wire {
	w := &Wire{
		cap: cap, inj: inj, local: local,
		socks:     map[uint16]*Socket{},
		done:      make(chan struct{}),
		readerEnd: make(chan struct{}),
	}
	w.learner, _ = inj.(NextHopLearner)
	go w.readLoop()
	return w
}

// SetForeignHook registers what is told when the previous PCK's handshake is
// seen on the wire. See foreign.go.
func (w *Wire) SetForeignHook(f func(src net.Addr)) {
	if f == nil {
		w.onForeign.Store(nil)
		return
	}
	w.onForeign.Store(&f)
}

// Stats returns a snapshot of the counters.
func (w *Wire) Stats() WireStats {
	// Packets before bytes is the order the l3 engine's counters use, for the
	// same reason: a reader that sees packets must see their bytes.
	return WireStats{
		PacketsIn: w.pktsIn.Load(), PacketsOut: w.pktsOut.Load(),
		BytesIn: w.bytesIn.Load(), BytesOut: w.bytesOut.Load(),
		RxDrops: w.rxDrops.Load(), Foreign: w.foreign.Load(), TxErrors: w.txErrs.Load(),
	}
}

func (w *Wire) readLoop() {
	defer close(w.readerEnd)
	for {
		payload, src, dport, mac, err := w.cap.Next()
		if err != nil {
			if errors.Is(err, errNoFrame) {
				select {
				case <-w.done:
					return
				default:
					continue
				}
			}
			// A capture that fails for real ends the wire: every socket's
			// reads fail, which ends its session, which the engine rebuilds.
			w.Close()
			return
		}
		select {
		case <-w.done:
			return
		default:
		}
		w.bytesIn.Add(uint64(len(payload)))
		w.pktsIn.Add(1)
		if mac != nil && w.learner != nil && w.learner.WantsNextHops() {
			w.learner.LearnNextHop(src.IP, mac)
		}
		if looksLikeOldPck(payload) {
			w.foreign.Add(1)
			if f := w.onForeign.Load(); f != nil {
				(*f)(src)
			}
			continue
		}
		w.mu.RLock()
		s := w.socks[dport]
		w.mu.RUnlock()
		if s == nil || len(payload) > frameBufLen {
			continue
		}
		// The datagram lane's packets are not KCP's: see datagram.go. The hook
		// must not keep the payload, which is only valid until the next read.
		if h := s.dgHook.Load(); h != nil && (*h)(src, payload) {
			continue
		}
		bp := framePool.Get().(*[]byte)
		n := copy(*bp, payload)
		select {
		case s.rx <- rxFrame{buf: bp, n: n, src: src}:
		default:
			framePool.Put(bp)
			w.rxDrops.Add(1)
		}
	}
}

// Open creates the socket for a local port.
func (w *Wire) Open(port uint16, onClose func()) (*Socket, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-w.done:
		return nil, net.ErrClosed
	default:
	}
	if _, dup := w.socks[port]; dup {
		return nil, errors.New("paqet: port already open on this wire")
	}
	s := &Socket{w: w, port: port, rx: make(chan rxFrame, rxQueueLen), die: make(chan struct{}), onClose: onClose}
	w.socks[port] = s
	return s, nil
}

// Close stops the reader, closes every socket and the capture and injector.
func (w *Wire) Close() error {
	w.closeOnce.Do(func() {
		close(w.done)
		w.mu.Lock()
		socks := make([]*Socket, 0, len(w.socks))
		for _, s := range w.socks {
			socks = append(socks, s)
		}
		w.mu.Unlock()
		for _, s := range socks {
			s.Close()
		}
		w.cap.Close()
		w.inj.Close()
	})
	return nil
}

// Done is closed when the wire has ended.
func (w *Wire) Done() <-chan struct{} { return w.done }

// Socket is a net.PacketConn on one port of a Wire, the thing KCP is given in
// place of a UDP socket.
type Socket struct {
	w    *Wire
	port uint16
	rx   chan rxFrame
	die  chan struct{}

	closeOnce sync.Once
	onClose   func()

	// dgHook, when set, is offered each arriving segment first and returns true
	// for one it consumed (a datagram of the lane).
	dgHook atomic.Pointer[func(src *net.UDPAddr, payload []byte) bool]

	// txErrs counts the segments this socket could not send. The window
	// control reads it as a sign that the local queue is full.
	txErrs atomic.Uint64
	// txOK counts the ones it did, for the share the errors are of the whole.
	txOK atomic.Uint64

	readDeadline, writeDeadline atomic.Value // time.Time
}

// SetDatagramHook registers what is offered the segments that arrive on this
// socket before KCP is. See datagram.go.
func (s *Socket) SetDatagramHook(f func(src *net.UDPAddr, payload []byte) bool) {
	if f == nil {
		s.dgHook.Store(nil)
		return
	}
	s.dgHook.Store(&f)
}

func (s *Socket) ReadFrom(data []byte) (int, net.Addr, error) {
	for {
		var timer <-chan time.Time
		var t *time.Timer
		if d, ok := s.readDeadline.Load().(time.Time); ok && !d.IsZero() {
			wait := time.Until(d)
			if wait <= 0 {
				return 0, nil, os.ErrDeadlineExceeded
			}
			t = time.NewTimer(wait)
			timer = t.C
		}
		select {
		case f := <-s.rx:
			if t != nil {
				t.Stop()
			}
			n := copy(data, (*f.buf)[:f.n])
			framePool.Put(f.buf)
			return n, f.src, nil
		case <-s.die:
			if t != nil {
				t.Stop()
			}
			return 0, nil, net.ErrClosed
		case <-timer:
			// Loop: the deadline may have been moved.
		}
	}
}

func (s *Socket) WriteTo(data []byte, addr net.Addr) (int, error) {
	select {
	case <-s.die:
		return 0, net.ErrClosed
	default:
	}
	if d, ok := s.writeDeadline.Load().(time.Time); ok && !d.IsZero() && !time.Now().Before(d) {
		return 0, os.ErrDeadlineExceeded
	}
	daddr, ok := addr.(*net.UDPAddr)
	if !ok {
		return 0, net.InvalidAddrError("invalid address")
	}
	if err := s.w.inj.Inject(s.port, data, daddr); err != nil {
		// A segment that could not be sent is a lost datagram, not a broken
		// socket. kcp-go closes a session whose WriteTo fails, and the usual
		// failure here — ENOBUFS, the transmit queue (or a netem/shaper in front
		// of it) full for an instant — is exactly what KCP exists to repair by
		// retransmitting. Returning it killed every session under load, found by
		// the throughput run on a delayed, lossy path. A wire that is really gone
		// ends the sessions through smux's keepalive instead, and the engine
		// reports the reason (see generation.watch).
		s.w.txErrs.Add(1)
		s.txErrs.Add(1)
		s.w.lastTxErr.Store(&err)
		return len(data), nil
	}
	s.txOK.Add(1)
	s.w.pktsOut.Add(1)
	s.w.bytesOut.Add(uint64(len(data)))
	return len(data), nil
}

func (s *Socket) Close() error {
	s.closeOnce.Do(func() {
		close(s.die)
		s.w.mu.Lock()
		if s.w.socks[s.port] == s {
			delete(s.w.socks, s.port)
		}
		s.w.mu.Unlock()
		if s.onClose != nil {
			s.onClose()
		}
	})
	return nil
}

func (s *Socket) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: s.w.local, Port: int(s.port)}
}
func (s *Socket) SetDeadline(t time.Time) error {
	s.readDeadline.Store(t)
	s.writeDeadline.Store(t)
	return nil
}
func (s *Socket) SetReadDeadline(t time.Time) error  { s.readDeadline.Store(t); return nil }
func (s *Socket) SetWriteDeadline(t time.Time) error { s.writeDeadline.Store(t); return nil }

// SetDSCP satisfies kcp-go's optional interface. The injector writes the field
// itself (paqet's choice of 184, DSCP EF), so there is nothing to set.
func (s *Socket) SetDSCP(int) error { return nil }
