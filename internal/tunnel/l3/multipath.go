package l3

import (
	"errors"
	"fmt"
	"github.com/backpack/backpack/internal/utils/network"
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
	paths   []DatagramCarrier
	next    atomic.Uint32
	state   []pathHealth
	sendMu  sync.Mutex
	queueMu sync.Mutex

	// The address reported upward, guarded by mu. It is one address for the
	// life of the tunnel, deliberately: the layer above follows a peer that
	// moves, and several paths arriving from several ports would otherwise read
	// as a peer moving on every packet. Each path knows its own peer; nothing
	// above needs to.
	//
	// The dialling side knows it at construction. The listening side does not —
	// it has nobody to report until somebody arrives — so it starts nil and is
	// pinned to the first address seen. That is not a detail: reporting nil is
	// what broke this carrier. The tunnel learns where its peer is from the
	// address handed up with a received packet (handleInit), and its outbound
	// pump drops every packet while that is nil. So with more than one path the
	// handshake completed, both ends logged an established session, and not one
	// byte of data could leave — a tunnel that reports itself healthy and
	// carries nothing.
	reported net.Addr

	in      chan pathPacket
	closed  chan struct{}
	allDead chan struct{}
	readers atomic.Int32
	once    sync.Once

	mu       sync.Mutex
	deadline time.Time
}

type pathHealth struct {
	retryAt atomic.Int64
	dead    atomic.Bool
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
		state:    make([]pathHealth, len(paths)),
		reported: reported,
		in:       make(chan pathPacket, pathQueue),
		closed:   make(chan struct{}),
		allDead:  make(chan struct{}),
	}
	c.readers.Store(int32(len(paths)))
	for i, p := range paths {
		go func(i int, p DatagramCarrier) {
			defer func() {
				if c.readers.Add(-1) == 0 {
					close(c.allDead)
				}
			}()
			c.pumpPath(i, p)
		}(i, p)
	}
	return c
}

// pump drains one path into the shared queue until it or the carrier closes.
// pumpPath preserves each path's batch capability and retries transient reads.
func (c *multipathCarrier) pumpPath(i int, p DatagramCarrier) {
	width := 1
	br := asBatchReader(p)
	// Peer pinning stays per datagram, while the path pump can batch the UDP reads.
	pinned, isPinned := p.(*pinnedCarrier)
	if isPinned {
		br = asBatchReader(pinned.DatagramCarrier)
	}
	if br != nil {
		width = batchSize
	}
	bufs := make([][]byte, width)
	sizes := make([]int, width)
	froms := make([]net.Addr, width)
	for j := range bufs {
		bufs[j] = make([]byte, maxMTU+256)
	}
	for {
		var count int
		var err error
		if br != nil {
			count, err = br.ReadBatch(bufs, sizes, froms)
		} else {
			sizes[0], froms[0], err = p.ReadFrom(bufs[0])
			if err == nil {
				count = 1
			}
		}
		for j := 0; j < count; j++ {
			if sizes[j] < 0 || sizes[j] > len(bufs[j]) {
				continue
			}
			if isPinned && froms[j] != nil {
				pinned.mu.Lock()
				pinned.peer = froms[j]
				pinned.mu.Unlock()
			}
			c.queueMu.Lock()
			select {
			case <-c.closed:
				c.queueMu.Unlock()
				return
			default:
			}
			if len(c.in) == cap(c.in) {
				c.queueMu.Unlock()
				continue
			}
			frame := network.GetDatagramBuffer(sizes[j])
			copy(frame, bufs[j][:sizes[j]])
			c.in <- pathPacket{frame, froms[j]}
			c.queueMu.Unlock()
		}
		if err == nil {
			c.state[i].retryAt.Store(0)
			continue
		}
		c.state[i].retryAt.Store(time.Now().Add(time.Second).UnixNano())
		if errors.Is(err, net.ErrClosed) {
			c.state[i].dead.Store(true)
			return
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-c.closed:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// availablePath chooses a usable path, with at most one probe per second after failure.
func (c *multipathCarrier) availablePath() int {
	start := int((c.next.Add(1) - 1) % uint32(len(c.paths)))
	now := time.Now().UnixNano()
	for offset := 0; offset < len(c.paths); offset++ {
		i := (start + offset) % len(c.paths)
		if !c.state[i].dead.Load() && now >= c.state[i].retryAt.Load() {
			return i
		}
	}
	return -1
}
func (c *multipathCarrier) pathFailed(i int, err error) {
	c.state[i].retryAt.Store(time.Now().Add(time.Second).UnixNano())
	if errors.Is(err, net.ErrClosed) {
		c.state[i].dead.Store(true)
	}
}

// WriteTo retries only an unsent datagram; a partial write is never replayed.
func (c *multipathCarrier) WriteTo(p []byte, _ net.Addr) (int, error) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	var last error
	for range len(c.paths) {
		i := c.availablePath()
		if i < 0 {
			break
		}
		n, err := c.paths[i].WriteTo(p, nil)
		if err == nil {
			return n, nil
		}
		c.pathFailed(i, err)
		last = err
		if n > 0 {
			return n, err
		}
	}
	if last == nil {
		last = fmt.Errorf("l3: no multipath route is currently available")
	}
	return 0, last
}

// WriteBatch keeps the reported prefix contiguous. Each chunk uses one path's
// sendmmsg/GSO capability; subsequent chunks rotate to the next usable path.
func (c *multipathCarrier) WriteBatch(bufs [][]byte, _ net.Addr) (int, error) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	sent := 0
	for sent < len(bufs) {
		select {
		case <-c.closed:
			return sent, net.ErrClosed
		default:
		}
		i := c.availablePath()
		if i < 0 {
			return sent, fmt.Errorf("l3: no multipath route is currently available")
		}
		end := min(sent+batchSize, len(bufs))
		var n int
		var err error
		if bw := asBatchWriter(c.paths[i]); bw != nil {
			n, err = bw.WriteBatch(bufs[sent:end], nil)
		} else {
			for sent+n < end {
				written, e := c.paths[i].WriteTo(bufs[sent+n], nil)
				if e != nil {
					err = e
					if written > 0 {
						return sent + n, e
					}
					break
				}
				n++
			}
		}
		if n < 0 || n > end-sent {
			return sent, fmt.Errorf("l3: invalid batch result")
		}
		sent += n
		if err != nil {
			c.pathFailed(i, err)
		} else if n == 0 {
			return sent, nil
		}
	}
	return sent, nil
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
		n := copy(p, pkt.data)
		network.PutDatagramBuffer(pkt.data)
		return n, c.stableAddr(pkt.addr), nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	case <-c.allDead:
		// Every pump has stopped, so nothing more can enter the queue. Deliver
		// its remaining packets before reporting the permanent carrier failure.
		select {
		case pkt := <-c.in:
			n := copy(p, pkt.data)
			network.PutDatagramBuffer(pkt.data)
			return n, c.stableAddr(pkt.addr), nil
		default:
			return 0, nil, net.ErrClosed
		}
	case <-timeout:
		return 0, nil, timeoutError{}
	}
}

// ReadBatch drains queued datagrams after the first, without a timer or extra latency.
func (c *multipathCarrier) ReadBatch(bufs [][]byte, sizes []int, froms []net.Addr) (int, error) {
	if len(bufs) == 0 {
		return 0, nil
	}
	n, addr, err := c.ReadFrom(bufs[0])
	if err != nil {
		return 0, err
	}
	sizes[0], froms[0] = n, addr
	count := 1
	for count < len(bufs) {
		select {
		case pkt := <-c.in:
			sizes[count] = copy(bufs[count], pkt.data)
			froms[count] = c.stableAddr(pkt.addr)
			network.PutDatagramBuffer(pkt.data)
			count++
		default:
			return count, nil
		}
	}
	return count, nil
}

// stableAddr is the address handed upward with every packet: the one fixed at
// construction, or — when there was none to fix, which is the listening side —
// the first one seen, kept from then on.
//
// Pinning rather than passing each packet's own address through is the point of
// the field: several paths arrive from several ports, and reporting each would
// read to the layer above as a peer moving on every packet.
func (c *multipathCarrier) stableAddr(seen net.Addr) net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reported == nil {
		c.reported = seen
	}
	return c.reported
}

// timeoutError is what a read deadline produces, in the shape callers test for.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func (c *multipathCarrier) Close() error {
	var err error
	c.once.Do(func() {
		c.queueMu.Lock()
		close(c.closed)
		c.queueMu.Unlock()
		for _, p := range c.paths {
			if e := p.Close(); e != nil && err == nil {
				err = e
			}
		}
		c.queueMu.Lock()
		defer c.queueMu.Unlock()
		for {
			select {
			case pkt := <-c.in:
				network.PutDatagramBuffer(pkt.data)
			default:
				return
			}
		}
	})
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
