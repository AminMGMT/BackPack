package l3

import (
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Several sockets instead of one, as a carrier that wraps carriers.
//
// A tunnel on one UDP socket is one 5-tuple, and a 5-tuple is what a shaper
// counts. Where a provider rate-limits per flow — which is the ordinary way a
// consumer link and a good many transit paths behave — the whole tunnel gets
// one flow's allowance no matter how much headroom the link has. Spreading the
// same traffic over several sockets makes it several flows, and several flows
// get several allowances. It also gives the kernel more than one queue to work
// with, which matters once the packet rate is high enough to be the limit.
//
// The ports are derived, not negotiated: the base port is the one in the
// configuration and the paths take the next ones in order, so both ends arrive
// at the same set from the same file. A tunnel with paths = 4 on port 9000 uses
// 9000-9003 and needs those open.
//
// # Why this is not offered for the spoof carrier
//
// It would add almost nothing there. The forged-source carrier already varies
// its source port on every packet (spoof_shuffle_port) and rotates through a
// pool of source addresses (spoof_src_pool), so a shaper counting flows already
// sees many. Adding sockets underneath that would multiply the machinery
// without multiplying the effect it is for.
//
// # What it does not do
//
// It adds nothing to the wire — no header, no identifier, so Overhead() is the
// carrier's own and the MTU is unchanged. It makes no attempt to balance by
// measured quality: paths are used in turn, which is what spreads a flow
// counter evenly and is the whole point. And it does not reorder deliberately,
// though several paths do arrive interleaved — the tunnel's replay window above
// is what absorbs that, and it is why paths are capped well below the width of
// that window.

// maxPaths caps how many sockets a tunnel may spread over.
//
// The ceiling is not arbitrary: every extra path widens the reordering the
// receiver sees, and the sealed layer above only accepts a counter inside its
// replay window. Eight interleaved paths is comfortably inside it, and a tunnel
// that needs more than eight flows to get its bandwidth has a problem no number
// of sockets will fix.
const maxPaths = 8

// pathQueue is how many received datagrams may wait ahead of the reader before
// the pumps start dropping. A datagram carrier is allowed to drop — the layer
// above is built for it — and dropping is better than a pump stalling and
// leaving one path's packets stuck behind another's.
const pathQueue = 1024

// MultipathConfig is how many sockets to spread over. Zero and one both mean a
// single socket, which is the ordinary tunnel and costs nothing.
type MultipathConfig struct {
	Paths int
}

// Enabled reports whether more than one socket was asked for.
func (m MultipathConfig) Enabled() bool { return m.Paths > 1 }

// Validate rejects a count the carrier cannot serve.
func (m MultipathConfig) Validate() error {
	if m.Paths < 0 {
		return fmt.Errorf("l3: paths cannot be negative (got %d)", m.Paths)
	}
	if m.Paths > maxPaths {
		return fmt.Errorf("l3: paths is %d; at most %d are supported, and a tunnel that "+
			"needs more flows than that to reach its bandwidth has a problem more sockets "+
			"will not fix", m.Paths, maxPaths)
	}
	return nil
}

// multipathCarrier spreads writes over several carriers and merges their reads.
type multipathCarrier struct {
	paths []DatagramCarrier
	next  atomic.Uint32

	// The address reported upward. It is one address for the life of the
	// tunnel, deliberately: the layer above follows a peer that moves, and
	// several paths arriving from several ports would otherwise read as a peer
	// moving on every packet. Each path knows its own peer; nothing above needs
	// to.
	reported net.Addr

	in     chan pathPacket
	closed chan struct{}
	once   sync.Once

	mu       sync.Mutex
	deadline time.Time
}

type pathPacket struct {
	data []byte
	addr net.Addr
}

// newMultipathCarrier merges the given carriers into one. A single carrier is
// returned untouched, so the ordinary tunnel pays nothing for this existing.
func newMultipathCarrier(paths []DatagramCarrier, reported net.Addr) DatagramCarrier {
	if len(paths) == 1 {
		return paths[0]
	}
	c := &multipathCarrier{
		paths:    paths,
		reported: reported,
		in:       make(chan pathPacket, pathQueue),
		closed:   make(chan struct{}),
	}
	for _, p := range paths {
		go c.pump(p)
	}
	return c
}

// pump drains one path into the shared queue until it or the carrier closes.
func (c *multipathCarrier) pump(p DatagramCarrier) {
	buf := make([]byte, 65535)
	for {
		n, addr, err := p.ReadFrom(buf)
		if err != nil {
			select {
			case <-c.closed:
				return
			default:
			}
			// A path that fails stops pumping; the others carry the tunnel. A
			// carrier is allowed to lose a path, and losing one is not a reason
			// to take the tunnel down.
			return
		}
		pkt := pathPacket{data: append([]byte(nil), buf[:n]...), addr: addr}
		select {
		case c.in <- pkt:
		case <-c.closed:
			return
		default:
			// The reader is behind. Drop, as any datagram carrier may.
		}
	}
}

// WriteTo sends on the next path in turn. The address is the caller's idea of
// the peer and is ignored: each path holds its own, which is what makes the
// several sockets several flows to the same place.
func (c *multipathCarrier) WriteTo(p []byte, _ net.Addr) (int, error) {
	i := int(c.next.Add(1)-1) % len(c.paths)
	return c.paths[i].WriteTo(p, nil)
}

// ReadFrom returns the next datagram from any path, honouring the read deadline
// the tunnel sets on it.
func (c *multipathCarrier) ReadFrom(p []byte) (int, net.Addr, error) {
	c.mu.Lock()
	dl := c.deadline
	c.mu.Unlock()

	var timeout <-chan time.Time
	if !dl.IsZero() {
		t := time.NewTimer(time.Until(dl))
		defer t.Stop()
		timeout = t.C
	}
	select {
	case pkt := <-c.in:
		// The address reported is the stable one, not the path's: see the
		// comment on the field.
		return copy(p, pkt.data), c.reported, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	case <-timeout:
		return 0, nil, timeoutError{}
	}
}

// timeoutError is what a read deadline produces, in the shape callers test for.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func (c *multipathCarrier) Close() error {
	c.once.Do(func() { close(c.closed) })
	var err error
	for _, p := range c.paths {
		if e := p.Close(); e != nil && err == nil {
			err = e
		}
	}
	return err
}

func (c *multipathCarrier) LocalAddr() net.Addr { return c.paths[0].LocalAddr() }

// Overhead is one path's: this layer writes nothing of its own on the wire, so
// the MTU is exactly what a single socket would carry.
func (c *multipathCarrier) Overhead() int { return c.paths[0].Overhead() }

func (c *multipathCarrier) CarrierName() string {
	return fmt.Sprintf("%s×%d", c.paths[0].CarrierName(), len(c.paths))
}

func (c *multipathCarrier) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}

func (c *multipathCarrier) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return nil
}

// SetWriteDeadline applies to every path, since a write goes to exactly one.
func (c *multipathCarrier) SetWriteDeadline(t time.Time) error {
	var err error
	for _, p := range c.paths {
		if e := p.SetWriteDeadline(t); e != nil && err == nil {
			err = e
		}
	}
	return err
}

// pathAddr returns an address with its port moved on by i, which is how both
// ends derive the same set of ports from one configured address.
func pathAddr(base string, i int) (string, error) {
	host, portStr, err := net.SplitHostPort(base)
	if err != nil {
		return "", fmt.Errorf("l3: %q needs a host and port to spread over several: %w", base, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", fmt.Errorf("l3: %q does not name a port", base)
	}
	if port+i > 65535 {
		return "", fmt.Errorf("l3: spreading over %d paths from port %d runs past 65535; "+
			"choose a lower tunnel port", i+1, port)
	}
	return net.JoinHostPort(host, strconv.Itoa(port+i)), nil
}
