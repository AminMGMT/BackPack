package paqet

import (
	"time"

	"github.com/backpack/backpack/internal/pathctl"
	"github.com/xtaci/kcp-go/v5"
)

// Window control: how much of the sessions' send window is used, how fast their
// output is paced, and how long their retransmission timers wait. The control
// itself, and the account of what it is for and how it was measured on this
// engine, live in internal/pathctl, which the reverse pck transport shares.
// This file is the engine's side of it: which sessions there are, and what the
// sockets they send on have refused.

// windowControl drives the send window of every session of a generation, which
// all take the same value.
type windowControl struct {
	*pathctl.Controller
}

func newWindowControl(g *generation, ceiling, rcv int) *windowControl {
	m := modeOf(g.e.p)
	return &windowControl{pathctl.New(pathctl.Config{
		Ceiling: ceiling, Rcv: rcv, MTU: g.e.p.MTU,
		Tick:         time.Duration(m.interval) * time.Millisecond,
		WantBatching: !m.ackNoDelay,
		Sessions: func() ([]pathctl.Session, uint64, uint64) {
			ks, socks := g.liveKCP()
			ss := make([]pathctl.Session, len(ks))
			for i, kc := range ks {
				ss[i] = kc
			}
			var tx, sent uint64
			for _, s := range socks {
				tx += s.txErrs.Load()
				sent += s.txOK.Load()
			}
			return ss, tx, sent
		},
	})}
}

// run adjusts the window until done closes.
func (c *windowControl) run(done <-chan struct{}) { c.Run(done) }

// liveKCP is the generation's KCP sessions that are up, and the sockets they
// send on (one on the server, one each on the client).
func (g *generation) liveKCP() (ks []*kcp.UDPSession, socks []*Socket) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, s := range g.sessions {
		if s == nil || s.kc == nil || s.sm == nil || s.sm.IsClosed() {
			continue
		}
		ks = append(ks, s.kc)
		if s.sock != nil {
			socks = append(socks, s.sock)
		}
	}
	if g.e.cfg.Server && len(ks) > 0 {
		socks = append(socks, g.ws.serverSocket)
	}
	return ks, socks
}

// WindowState is the engine's window control as it stands, for Stats and the
// soak harness.
type WindowState struct {
	// Window is what every session's send window is at, Ceiling the preset's.
	Window, Ceiling int
	// SRTT is the smoothed round trip the controller reads, Base the lowest it
	// has seen lately.
	SRTT, Base time.Duration
	// Cuts counts the times a queue or an overflow shrank the window.
	Cuts uint64
}

// Window reports the window control of the running generation.
func (e *Engine) Window() (WindowState, bool) {
	e.mu.RLock()
	g := e.gen
	e.mu.RUnlock()
	if g == nil || g.ctl == nil {
		return WindowState{}, false
	}
	c := g.ctl
	return WindowState{Window: c.Window(), Ceiling: c.Ceiling(), SRTT: c.SRTT(), Base: c.Base(), Cuts: c.Cuts()}, true
}
