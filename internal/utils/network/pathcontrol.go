package network

import (
	"net"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/pathctl"
	"github.com/xtaci/kcp-go/v5"
)

// PathControl is the pck transport's send-side control of its KCP sessions: the
// window controller, the output pace, the retransmission margin and the
// acknowledgement batching of internal/pathctl, which the direct pck engine
// runs too, and fast retransmission of a lost fast retransmit. See that package
// for what each is for and the measurements behind it.
//
// # Why reverse pck needed it
//
// The presets give every pooled session a send window of 512 to 2048 packets
// with KCP's own congestion window off (kcp_nocongestion = 1), and a Turbo pool
// is eight of them: 8 x 1024 packets can be in flight against a path that holds
// perhaps 1500. The window was the only brake, so a bulk transfer filled the
// bottleneck's queue and kept it full, and everything sharing the tunnel waited
// behind it: on the soak path with no random loss a third of the segments were
// sent by the retransmission timer and the probe took 1.3 s. What it changes is
// only what this end sends and when, so a peer without it (v1.9.0 and earlier)
// cannot tell and nothing is negotiated.
//
// # What is different from the direct engine
//
// The pool has eight sessions where the direct engine has two, and most of a
// load's bytes ride on a few of them. Two settings of the controller are for
// that: the window it settles at is a budget for the tunnel, divided among the
// sessions that have something to send (pathctl.Config.SplitBusy), and a session
// that has received nothing recently does not vote on the queue of the way back
// (QuietAbstain). Without them the busy sessions had a few per cent of the path
// between them, and which direction that was came and went from run to run.
// Acknowledgements are batched where the round trip is longer than the tick,
// whatever kcp_acknodelay says: the presets' immediate acknowledgements were
// half of the packets the bottleneck queue counted.
//
// # Scope
//
// Opt-in per transport: only a KCPSettings whose Path is set gets any of it, and
// only the pck transport sets one (EnablePathControl). kcp and xdi share every
// line of the session code and keep their old behaviour.
//
// One PathControl serves one tunnel, all of its pooled sessions together: they
// share a path, and so one verdict on it. The control channel is not one of them
// (it carries a heartbeat a few times a minute and has no window to manage).
// The controller runs while there is a session to drive and starts over, from
// slow start, when the pool has been empty: it is then a new connection to a
// path that may have changed.
type PathControl struct {
	ceiling, rcv int
	mtu          int
	tick         time.Duration

	mu  sync.Mutex
	ctl *pathctl.Controller
	// stop closes when the last session leaves, ending the controller's goroutine.
	stop    chan struct{}
	members map[pathctl.Session]*pathMember
	// carriers are the sockets the sessions send on, counted once each however
	// many sessions share one (the server's one socket carries them all), and
	// retired holds what a closed socket had counted so the totals only rise.
	carriers       map[txStater]int
	retiredRefused uint64
	retiredSent    uint64
}

type pathMember struct{ carrier txStater }

// txStater is implemented by a carrier that can say how many segments its local
// transmit queue refused, and how many it took (the pck connection).
type txStater interface{ TxStats() (refused, sent uint64) }

// EnablePathControl switches the path control on for the sessions made from s.
// It copies nothing it does not need from s, so it is called after the window,
// MTU and interval are filled in.
func EnablePathControl(s *KCPSettings) {
	s.Path = &PathControl{
		ceiling: s.SndWnd, rcv: s.RcvWnd, mtu: s.effectiveMTU(),
		tick: time.Duration(s.Interval) * time.Millisecond,

		members:  map[pathctl.Session]*pathMember{},
		carriers: map[txStater]int{},
	}
}

// add puts a session under control. carrier is the socket it sends on, or nil.
// The session takes the window the controller is at, so one that comes up late
// does not start from the preset's.
func (p *PathControl) add(s pathctl.Session, carrier any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.members[s]; ok {
		return
	}
	m := &pathMember{}
	if ts, ok := carrier.(txStater); ok {
		m.carrier = ts
		p.carriers[ts]++
	}
	p.members[s] = m
	if p.ctl == nil {
		p.ctl = pathctl.New(pathctl.Config{
			Ceiling: p.ceiling, Rcv: p.rcv, MTU: p.mtu, Tick: p.tick, WantBatching: true, QuietAbstain: true, SplitBusy: true, RevTolerant: true,
			Sessions: p.snapshot,
		})
		p.stop = make(chan struct{})
		go p.ctl.Run(p.stop)
	}
	s.SetWindowSize(p.ctl.Window(), p.rcv)
}

// remove takes a session out of control. When it was the last, the controller
// stops.
func (p *PathControl) remove(s pathctl.Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	m, ok := p.members[s]
	if !ok {
		return
	}
	delete(p.members, s)
	if m.carrier != nil {
		if p.carriers[m.carrier]--; p.carriers[m.carrier] <= 0 {
			r, o := m.carrier.TxStats()
			p.retiredRefused += r
			p.retiredSent += o
			delete(p.carriers, m.carrier)
		}
	}
	if len(p.members) == 0 && p.ctl != nil {
		close(p.stop)
		p.ctl, p.stop = nil, nil
	}
}

// snapshot is the controller's view each tick: the sessions that are up and the
// totals of what their sockets refused and took.
func (p *PathControl) snapshot() ([]pathctl.Session, uint64, uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ss := make([]pathctl.Session, 0, len(p.members))
	for s := range p.members {
		ss = append(ss, s)
	}
	refused, sent := p.retiredRefused, p.retiredSent
	for c := range p.carriers {
		r, o := c.TxStats()
		refused += r
		sent += o
	}
	return ss, refused, sent
}

// Window is the send window the controller has every session at now, 0 when no
// session is under control.
func (p *PathControl) Window() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctl == nil {
		return 0
	}
	return p.ctl.Window()
}

// Ceiling is the preset's window, the most any session is given.
func (p *PathControl) Ceiling() int { return p.ceiling }

// PooledKCP is IdleAwareKCP for a pooled session of a tunnel: the same idle
// governor, and the session is put under the tunnel's path control when it has
// one. Closing the returned conn releases both.
func PooledKCP(session *kcp.UDPSession, s KCPSettings) net.Conn {
	c := newIdleKCP(session, s, s.AckNoDelay)
	if s.Path != nil {
		c.path = s.Path
		s.Path.add(c, session.PacketConn())
	}
	return c
}
