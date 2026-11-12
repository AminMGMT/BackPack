package paqet

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/smux"
)

// A forwarded connection as the forwarder sees it.
//
// Both kinds are a smux stream whose first reply byte is the server's verdict on
// the target. The request is written and data may follow at once — the server
// dials while the bytes queue, so a new connection costs no extra round trip —
// and the verdict is read lazily, with the first Read. That is where a refused
// target surfaces, and why the forwarder is handed a report callback rather than
// an error from Dial: it cannot be known yet.

// StatusPending is implemented by connections whose first read carries the
// server's verdict on the target. The forwarder reads it to know it should not
// call a backend reachable merely because the stream opened.
type StatusPending interface{ StatusPending() bool }

// statusReader is the lazy verdict, shared by both kinds.
type statusReader struct {
	once   sync.Once
	pend   atomic.Bool
	err    error
	report func(error)
}

func (s *statusReader) consume(r io.Reader) error {
	s.once.Do(func() {
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			s.err = err
		} else if b[0] != statusOK {
			s.err = statusError(b[0])
		}
		s.pend.Store(true)
		if s.report != nil {
			s.report(s.err)
		}
	})
	return s.err
}

// tcpConn is a stream carrying one TCP connection: framed, so that each
// direction can end on its own (CloseWrite; see framed.go).
type tcpConn struct {
	*framedStream
	st statusReader
}

func (c *tcpConn) Read(p []byte) (int, error) {
	if err := c.st.consume(c.Stream); err != nil {
		return 0, err
	}
	return c.framedStream.Read(p)
}

// WriteTo is here because io.Copy prefers it to Read, and the framed stream's
// would not read the verdict first.
func (c *tcpConn) WriteTo(w io.Writer) (int64, error) {
	if err := c.st.consume(c.Stream); err != nil {
		return 0, err
	}
	return c.framedStream.WriteTo(w)
}

// StatusPending is true until the verdict has been read.
func (c *tcpConn) StatusPending() bool { return !c.st.pend.Load() }

// datagramConn is one UDP flow, a datagram per Read and per Write.
//
// Its stream opens the flow and is its lifetime: the request, the server's
// verdict, the close. The datagrams that fit one segment travel beside the
// stream, on the session's datagram lane (datagram.go), unreliable and
// unqueued; the ones that do not fit, and any sent before the verdict is in,
// are framed into the stream, which is reliable and in order but waits its
// turn (datagramsend.go). Both arrive in the same queue, and a Read takes the
// next from it, so a reader cannot tell which way a datagram came.
type datagramConn struct {
	*smux.Stream
	st statusReader
	// flow is where this flow's datagrams wait; lane is nil when the session
	// has none, and every datagram then uses the stream.
	flow *dgFlow
	lane *dgLane
	// out writes the datagrams that use the stream; Write only queues them.
	out *datagramSender

	rd atomic.Value // time.Time: the read deadline
}

// newDatagramConn starts a flow on st and the goroutine that reads the
// verdict and then what the stream carries.
func newDatagramConn(st *smux.Stream, report func(error), lane *dgLane, out *datagramSender) *datagramConn {
	c := &datagramConn{Stream: st, st: statusReader{report: report}, flow: newDGFlow(), lane: lane, out: out}
	if lane != nil {
		lane.mux.add(st.ID(), c.flow)
	}
	go c.pump()
	return c
}

// verdictWait bounds the wait for the server's verdict on a UDP flow.
const verdictWait = 30 * time.Second

// pump reads the verdict, then the framed datagrams of the stream, into the
// flow's queue.
func (c *datagramConn) pump() {
	c.Stream.SetReadDeadline(time.Now().Add(verdictWait))
	if err := c.st.consume(c.Stream); err != nil {
		c.flow.fail(err)
		return
	}
	c.Stream.SetReadDeadline(time.Time{})
	pumpFrames(c.Stream, c.flow)
}

// pumpFrames reads datagram frames from r into f until r ends.
func pumpFrames(r io.Reader, f *dgFlow) {
	var mu sync.Mutex
	var pending []byte
	buf := make([]byte, maxDatagram)
	for {
		n, err := readFramed(&mu, &pending, r, buf)
		if err != nil {
			f.fail(err)
			return
		}
		if !f.pushWait(append([]byte(nil), buf[:n]...)) {
			return
		}
	}
}

func (c *datagramConn) StatusPending() bool { return !c.st.pend.Load() }

// WriteTo hides the stream's own for the reason tcpConn's exists: a datagram
// conn must be read a datagram at a time, whoever copies from it.
func (c *datagramConn) WriteTo(w io.Writer) (int64, error) {
	return io.Copy(w, struct{ io.Reader }{c})
}

// Write sends one datagram and returns without waiting for the tunnel. On the
// lane, a datagram the local transmit queue has no room for is lost, as on any
// UDP path; on the stream it is queued, and dropped only when the flow's queue
// is full (datagramsend.go).
func (c *datagramConn) Write(p []byte) (int, error) {
	if c.lane != nil && c.st.pend.Load() && c.st.err == nil && c.lane.send(c.Stream.ID(), p) {
		return len(p), nil
	}
	if err := c.out.send(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Read returns the next datagram, from the lane or the stream.
func (c *datagramConn) Read(p []byte) (int, error) {
	d, _ := c.rd.Load().(time.Time)
	return c.flow.read(p, d)
}

func (c *datagramConn) SetReadDeadline(t time.Time) error { c.rd.Store(t); return nil }

func (c *datagramConn) SetDeadline(t time.Time) error {
	c.rd.Store(t)
	return c.Stream.SetWriteDeadline(t)
}

// Close stops the flow's writer, the lane's claim on it, and the stream.
func (c *datagramConn) Close() error {
	c.out.close()
	if c.lane != nil {
		c.lane.mux.remove(c.Stream.ID())
	}
	c.flow.fail(net.ErrClosed)
	return c.Stream.Close()
}

// readFramed returns the next whole datagram, reading more of the stream into
// *pending as needed. A datagram longer than p is dropped whole.
func readFramed(mu *sync.Mutex, pending *[]byte, r io.Reader, p []byte) (int, error) {
	mu.Lock()
	defer mu.Unlock()
	var tmp [4096]byte
	for {
		if buf := *pending; len(buf) >= 2 {
			n := int(buf[0])<<8 | int(buf[1])
			if len(buf) >= 2+n {
				*pending = buf[2+n:]
				if n > len(p) {
					continue
				}
				copy(p, buf[2:2+n])
				return n, nil
			}
		}
		k, err := r.Read(tmp[:])
		*pending = append(*pending, tmp[:k]...)
		if err != nil && k == 0 {
			return 0, err
		}
		if err != nil && !errors.Is(err, io.EOF) {
			// Data arrived with the error; surface the datagram first if it
			// is complete, the error on the next call.
			continue
		}
	}
}

var _ net.Conn = (*tcpConn)(nil)
var _ net.Conn = (*datagramConn)(nil)
