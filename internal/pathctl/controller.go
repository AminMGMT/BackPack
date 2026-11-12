package pathctl

import (
	"slices"
	"sync/atomic"
	"time"
)

// Session is what the controller reads of a KCP session and sets on it.
// *kcp.UDPSession (the fork in internal/thirdparty/kcp-go) satisfies it, and so
// does a wrapper that decides for itself when a setting may be applied.
type Session interface {
	// GetSRTT is KCP's smoothed round trip in milliseconds, 0 for none.
	GetSRTT() int32
	// TakeRecvDelay is the lowest one-way reading of the data received since
	// the last call (see kcp-go's sess.go).
	TakeRecvDelay() (ms int64, ok bool)
	SetWindowSize(sndwnd, rcvwnd int)
	SetRTOMargin(d time.Duration)
	SetRateLimit(bytesPerSecond uint32)
	SetACKNoDelay(nodelay bool)
	SetWriteDelay(delay bool)
}

// backlogger is what a session offers to be told apart as busy: how many
// packets it has queued or in flight. *kcp.UDPSession does.
type backlogger interface{ Backlog() int }

// busyFor is how long a session that had something to send still counts as
// busy, when the window is split among the busy ones.
const busyFor = 300 * time.Millisecond

// Config is what a Controller is built from.
type Config struct {
	// Ceiling is the preset's send window, the most any session is given;
	// Rcv is the receive window, set again with every change and not moved.
	Ceiling, Rcv int
	// MTU is the size of a KCP packet, for the pace.
	MTU int
	// Tick is the KCP tick, and WantBatching whether the mode batches
	// acknowledgements and small writes at all (batchingFor).
	Tick         time.Duration
	WantBatching bool
	// QuietAbstain leaves a session that has received nothing for half a second
	// out of the vote on how long the queue of the way back is. The vote is the
	// lowest any session reads, so that one session's own high reading does not
	// hide the queue; with many sessions, some of which a transfer never uses, a
	// silent one reads no queue and the vote is always zero, and the end whose
	// way back is loaded then counts that queue as its own and cuts for it. The
	// direct engine, with two sessions, does not need it and does not set it.
	QuietAbstain bool
	// SplitBusy reads the window as a budget for the whole tunnel and divides
	// it among the sessions that have something to send, instead of giving
	// every session the same window whether it is used or not. The control
	// cuts on the queue the sessions together build, so the window it settles
	// at is what N sessions in use can have between them; given to the two
	// that happen to carry a transfer, out of eight, it left them a quarter
	// of what the path held, and which two it was decided which direction was
	// starved. Two sessions, as in the direct engine, are nearly always both in
	// use and the split makes no difference there; it does not set this.
	SplitBusy bool
	// RevTolerant widens the queue the control tolerates by half the queue it
	// reads on the way back. That reading is taken from data the peer sends, and
	// a peer that does not run this control (the previous release) lets its own
	// direction fill its bottleneck queue, which then swings by hundreds of
	// milliseconds: the round trip less that queue is noise as large as the
	// slack, and the control cut on it until the window was at its floor, an
	// eighth of what the same end carried beside an old peer. With a peer that
	// runs the control the queue of the way back is tens of milliseconds and
	// this adds a few. The direct engine, whose peers are all of one release,
	// does not set it.
	RevTolerant bool
	// Sessions is called every tick for the sessions that are up, and the
	// running counts of the segments the local transmit queue refused and took
	// (summed over the sockets they send on). Zero counts mean the carrier
	// reports none.
	Sessions func() (ss []Session, txErrs, txOK uint64)
}

// Controller drives the send window of every session it is given, which all
// take the same value. See the comment at the top of this file's package.
type Controller struct {
	cfg Config

	window atomic.Int64 // current send window of each session, packets
	srtt   atomic.Int64 // microseconds
	base   atomic.Int64 // microseconds
	cuts   atomic.Uint64
}

// New builds a controller. Run drives it.
func New(cfg Config) *Controller {
	c := &Controller{cfg: cfg}
	c.window.Store(int64(newCtlState(cfg.Ceiling).w))
	return c
}

// Window is what every session's send window is at, in packets.
func (c *Controller) Window() int { return int(c.window.Load()) }

// Ceiling is the most a session's window is given: the preset's.
func (c *Controller) Ceiling() int { return c.cfg.Ceiling }

// SRTT is the smoothed round trip the controller reads, of its own direction.
func (c *Controller) SRTT() time.Duration { return time.Duration(c.srtt.Load()) * time.Microsecond }

// Base is the lowest round trip it has seen lately.
func (c *Controller) Base() time.Duration { return time.Duration(c.base.Load()) * time.Microsecond }

// Cuts counts the times a queue or an overflow shrank the window.
func (c *Controller) Cuts() uint64 { return c.cuts.Load() }

// Run adjusts the window until done closes.
func (c *Controller) Run(done <-chan struct{}) {
	t := time.NewTicker(ctlTick)
	defer t.Stop()

	st := newCtlState(c.cfg.Ceiling)
	st.revTolerant = c.cfg.RevTolerant
	path := newPathReader()
	path.abstain = c.cfg.QuietAbstain
	nextProbe := time.Now().Add(probeEvery)
	var probeUntil time.Time
	lastTick := time.Now()
	batching := c.cfg.WantBatching
	sessions := map[Session]*applied{}
	busy := map[Session]time.Time{}

	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		now := time.Now()
		dt := now.Sub(lastTick)
		lastTick = now

		ks, tx, sent := c.cfg.Sessions()
		if len(ks) == 0 {
			continue
		}
		in := ctlInput{now: now, dt: dt, probing: now.Before(probeUntil)}
		srcs := make([]delaySource, len(ks))
		for i, kc := range ks {
			srcs[i] = kc
		}
		path.read(&in, srcs)
		if !in.probing && !st.slow && now.After(nextProbe) {
			probeUntil = now.Add(probeFor)
			nextProbe = probeUntil.Add(probeEvery)
			in.probing = true
		}
		in.tx, in.sent = tx, sent
		c.srtt.Store(in.cur.Microseconds())
		c.base.Store(in.base.Microseconds())

		w := int64(st.decide(in))
		c.cuts.Store(st.cuts)
		if in.probing {
			w = max(int64(st.floor), w/2)
		}
		if c.cfg.SplitBusy {
			w = split(w, ks, busy, now, int64(st.floor), int64(st.ceiling))
		}
		if w != c.window.Load() {
			c.window.Store(w)
			for _, kc := range ks {
				kc.SetWindowSize(int(w), c.cfg.Rcv)
			}
		}

		if c.cfg.WantBatching {
			batching = batchingFor(batching, in.sample, in.base, c.cfg.Tick)
		}
		margin := retransmitMargin(in)
		rate := paceRate(in, w, c.cfg.MTU)
		for _, kc := range ks {
			a := sessions[kc]
			fresh := a == nil
			if fresh {
				a = &applied{batching: c.cfg.WantBatching}
				sessions[kc] = a
			}
			// A session that came up since the last look starts as the mode
			// table has it, whatever the path is like.
			if c.cfg.WantBatching && (fresh || a.batching != batching) {
				kc.SetACKNoDelay(!batching)
				kc.SetWriteDelay(batching)
				a.batching = batching
			}
			if a.margin != margin && (fresh || absDur(a.margin-margin) >= marginStep) {
				kc.SetRTOMargin(margin)
				a.margin = margin
			}
			if a.rate != rate && (fresh || rate == 0 || a.rate == 0 || rate*20 < a.rate*19 || rate*19 > a.rate*20) {
				kc.SetRateLimit(rate)
				a.rate = rate
			}
		}
		if len(sessions) > len(ks) {
			for kc := range sessions {
				if !slices.Contains(ks, kc) {
					delete(sessions, kc)
				}
			}
		}
	}
}

// split is the window each session has when w is the window of every one of
// them and only the busy ones use theirs: w times the sessions there are, over
// the ones that are busy, between floor and ceiling. busy remembers when each
// session was last seen with something to send.
func split(w int64, ks []Session, busy map[Session]time.Time, now time.Time, floor, ceiling int64) int64 {
	n := 0
	for _, kc := range ks {
		if b, ok := kc.(backlogger); ok && b.Backlog() > 0 {
			busy[kc] = now
		}
		if now.Sub(busy[kc]) <= busyFor {
			n++
		}
	}
	if len(busy) > len(ks) {
		for kc := range busy {
			if !slices.Contains(ks, kc) {
				delete(busy, kc)
			}
		}
	}
	if n < 1 {
		n = 1
	}
	return min(ceiling, max(floor, w*int64(len(ks))/int64(n)))
}
