package network

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/kcp-go/v5"
)

// An idle KCP session still wakes up every kcp_interval milliseconds.
//
// kcp-go flushes each session on a timer whether or not it has anything to
// send, so a pool of eight sessions at the Turbo preset's 10 ms is 800 wake-ups
// a second on a tunnel carrying nothing. Even with one scheduler (kcpsched.go)
// that cost 3% of a core per process, idle, on KCP, pck and xdi alike, against
// 0.15% for every TCP transport. The profile is the Go scheduler and the clock:
// the flushes themselves find nothing to do.
//
// So a session that has carried nothing for idleAfter is moved to
// idleInterval, and moved back the moment it reads or writes again — pool
// sessions and the control channel alike. Measured on a reverse kcp, pck and
// xdi pair with an eight-session pool: idle 3% → 1.4% of a core per process,
// and the same throughput and CPU per byte under load.
//
// Two things make the slow interval safe to leave in place until traffic
// returns:
//
//   - A Write flushes immediately (SetWriteDelay(false)), so the first bytes
//     after an idle spell go out at once; the interval only paces
//     retransmission and what is left in the queue.
//   - While idle the session acknowledges every packet as it arrives
//     (SetACKNoDelay(true)). Otherwise the far end's first packet would wait
//     up to idleInterval for its ack and be retransmitted — and counted as
//     loss — before the ack arrived. It also keeps keepalive round trips from
//     inflating the RTT estimate while nothing else is sent.
//
// What it does cost: a segment lost in the first idleInterval after an idle
// spell is retransmitted up to idleInterval late. Once.
const (
	idleAfter    = 3 * time.Second
	idleInterval = 200
	idleSweep    = time.Second
)

// kcpTuner is the part of a *kcp.UDPSession the governor changes.
type kcpTuner interface {
	SetNoDelay(nodelay, interval, resend, nc int)
	SetACKNoDelay(nodelay bool)
}

// IdleAwareKCP wraps a tuned KCP session so that it slows its flush timer
// while it carries nothing. ackNoDelay is what the session acknowledges with
// while it is busy — the tunnel's setting, or true for a session that forces
// it, such as the control channel.
func IdleAwareKCP(session *kcp.UDPSession, s KCPSettings, ackNoDelay bool) net.Conn {
	c := &idleKCP{UDPSession: session, tuner: session, interval: s.Interval, ack: ackNoDelay}
	c.touch()
	idleGov.add(c)
	return c
}

type idleKCP struct {
	*kcp.UDPSession
	tuner    kcpTuner
	interval int  // the tunnel's own kcp_interval
	ack      bool // the tunnel's own ack_nodelay

	last atomic.Int64 // unix nanoseconds of the last read or write
	idle atomic.Bool
	mu   sync.Mutex // orders the switches themselves
}

func (c *idleKCP) Read(b []byte) (int, error) {
	n, err := c.UDPSession.Read(b)
	if n > 0 {
		c.touch()
	}
	return n, err
}

func (c *idleKCP) Write(b []byte) (int, error) {
	// Before the write, so that the write is the one that goes out fast.
	c.touch()
	return c.UDPSession.Write(b)
}

func (c *idleKCP) Close() error {
	idleGov.remove(c)
	return c.UDPSession.Close()
}

// touch records activity and, if the session was idle, puts it back on the
// tunnel's own interval.
//
// The store comes before the load on purpose, and the sweep does the opposite
// (sets idle, then re-reads last): whichever of the two runs second sees the
// other's write, so a session can never be left idle across a read or write.
func (c *idleKCP) touch() {
	c.last.Store(time.Now().UnixNano())
	if c.idle.Load() {
		c.mu.Lock()
		if c.idle.Load() {
			c.idle.Store(false)
			c.busy()
		}
		c.mu.Unlock()
	}
}

// sweep moves the session to the idle interval if it has been quiet for after.
func (c *idleKCP) sweep(now time.Time, after time.Duration) {
	quietSince := now.Add(-after).UnixNano()
	if c.idle.Load() || c.last.Load() > quietSince {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idle.Load() {
		return
	}
	c.idle.Store(true)
	c.tuner.SetACKNoDelay(true)
	c.tuner.SetNoDelay(-1, idleInterval, -1, -1)
	if c.last.Load() > quietSince {
		// Traffic arrived while it was being switched.
		c.idle.Store(false)
		c.busy()
	}
}

// busy restores the tunnel's own settings. Called with mu held.
func (c *idleKCP) busy() {
	c.tuner.SetNoDelay(-1, c.interval, -1, -1)
	c.tuner.SetACKNoDelay(c.ack)
}

// idleGov is the one goroutine that looks for quiet sessions, once per
// idleSweep for all of them — a wake-up a second, against hundreds.
var idleGov = &idleGovernor{conns: map[*idleKCP]struct{}{}, after: idleAfter, every: idleSweep}

type idleGovernor struct {
	mu      sync.Mutex
	conns   map[*idleKCP]struct{}
	running bool
	after   time.Duration // idleAfter, or shorter in a test
	every   time.Duration // idleSweep, or shorter in a test
}

func (g *idleGovernor) add(c *idleKCP) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.conns[c] = struct{}{}
	if !g.running {
		g.running = true
		go g.run()
	}
}

func (g *idleGovernor) remove(c *idleKCP) {
	g.mu.Lock()
	delete(g.conns, c)
	g.mu.Unlock()
}

// run sweeps until there is nothing left to sweep, so a process whose last
// KCP session has closed has no ticker left behind.
func (g *idleGovernor) run() {
	g.mu.Lock()
	every := g.every
	g.mu.Unlock()
	t := time.NewTicker(every)
	defer t.Stop()
	for now := range t.C {
		g.mu.Lock()
		if len(g.conns) == 0 {
			g.running = false
			g.mu.Unlock()
			return
		}
		conns := make([]*idleKCP, 0, len(g.conns))
		for c := range g.conns {
			conns = append(conns, c)
		}
		after := g.after
		if g.every != every {
			every = g.every
			t.Reset(every)
		}
		g.mu.Unlock()
		for _, c := range conns {
			c.sweep(now, after)
		}
	}
}
