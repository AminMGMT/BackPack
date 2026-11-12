// Package pathctl is the send-side control of a set of KCP sessions that share
// one path: how much of their send window is used, how fast their output is
// paced, how long their retransmission timers wait, and whether acknowledgements
// are batched.
//
// It was written for the direct pck engine (internal/tunnel/paqet), which still
// runs it, and is shared with the reverse pck transport (internal/utils/network,
// PathControl). Everything it does is on the sending side and none of it changes
// the wire, so a peer that does not run it cannot tell. The comment below is the
// design; it says "the engine" where the direct engine's measurements are
// quoted.
//
// Window control: how much of the sessions' send window is used.
//
// # The problem this answers
//
// paqet runs KCP with congestion control off, and Backpack's presets inherited
// that with windows of 256 to 4096 packets. The window is then the only brake,
// and on a path whose bottleneck is a queue (every real one) a window larger
// than the path's bandwidth-delay product plus that queue sends more than the
// queue holds. The soak harness measured exactly that on a 100 Mbit/s, 30 ms
// path with a 1000-packet queue and no random loss: Turbo (2 sessions x 1024)
// had 2048 packets in flight against a path that holds about 1500, the
// bottleneck tail-dropped a quarter of what was sent (7.59 M segments sent, 5.49
// M received), KCP retransmitted 2.3 M segments (30%) and still delivered 14
// Mbit/s of a 100 Mbit/s link, with the queue never empty: the probe's latency
// was 661 ms (130 ms without the tunnel), 280 000 segments a minute were
// refused by the local transmit queue (ENOBUFS), and a stream could stall 15
// seconds. Everything that shared the session paid for it, small connections
// and forwarded UDP most of all.
//
// KCP's own congestion control (nc=0) is the textbook answer and the wrong
// one here: it is loss-based and halves on every loss, so on a path that loses
// 1-10% of packets at random, which is where PCK is wanted, it collapses to
// what a single TCP flow would carry. The tunnel would then lose to GRE at
// exactly the loss rates where it is the better transport.
//
// # What it does
//
// One controller per generation moves the send window of all its sessions
// together, between a floor and the preset's window, which is now the ceiling
// and not the operating point. The sessions share one path and so one verdict
// on it: a controller per session was tried first and handed one session the
// whole path and left the other at its floor. It reads two things, and not
// loss, and both are of its own direction only.
//
//   - Delay. Each session's smoothed RTT is smoothed again over rttTau (KCP's
//     own follows the last few acks, so on a path with 10 ms of jitter it wanders
//     by as much with no queue as with one), and base is the lowest of that over
//     the last baseWindow: the path with no queue of ours on it. A queue is
//     what stays above base by more than slack = max(slackMin, base/slackFrac).
//     An RTT holds the queue of both ways, so the queue of the way back,
//     measured where the data arrives (TakeRecvDelay, pathReader.read), is
//     taken off it: otherwise the less loaded end cut its window for the other
//     end's queue.
//   - The local transmit queue. A segment the injector could not send (ENOBUFS)
//     is an overflow that no RTT shows yet. It counts only once the refusals are
//     a share of the segments sent (txErrsPerMille, 1%): a burst that refuses
//     a few in thousands is not an overflow.
//
// It also sets two things the readings give it. The sessions' output is paced
// at paceGain x window x MTU / RTT, so a window is not sent as one burst into
// the bottleneck's queue, and their retransmission timers wait a margin beyond
// KCP's own, 2 x slack plus the reverse queue (retransmitMargin), so that a
// segment that is only queued is not sent again. A lost fast retransmit is
// itself fast-retransmitted (SetFastRetransmitAgain, kcpconn.go) and does not
// wait for the timer. All three need the fork of kcp-go in
// internal/thirdparty/kcp-go, and none changes the wire.
//
// It starts at startWindow and doubles per RTT until the first sign of a
// queue, then holds, and every stepEvery looks again: a queue (or errors) cuts
// the window towards what the path held, at most to three quarters; a clear
// path (less than half the slack) grows it by a few percent; in between it
// leaves it. A cut is followed by cutWait of not looking, because what was sent
// before it is still arriving: without the pause a burst of errors cut the
// window at every step down to its floor, and the climb back took half a minute.
//
// With bulk transfers running the queue never empties, so the lowest RTT seen
// creeps up to include the queue the controller itself leaves, and the line it
// measures against moves with it. Every probeEvery the window is halved for
// probeFor, which empties the queue, and the lowest RTT read then is the
// path's. It costs about 2% of the time.
//
// Random loss moves neither signal, so it does not shrink the window, and it
// should not: with a lost packet at the head of the window KCP cannot send past
// snd_una+window, so on a lossy path the window must be several times the path's
// BDP to keep the pipe full. The controller gets there by itself, since the
// packets actually in flight are fewer than the window, and the queue does not
// form until they are enough. That is what keeps the tunnel's advantage on a
// lossy path.
//
// What it does not see: a bottleneck that drops without queueing (a policer)
// shows in neither signal, and there the preset's window is the only limit, as
// before. The ceiling is therefore not a figure to raise without a reason. A
// signal from the share of segments lost was tried for it and removed: it fired
// at 10% random loss and cost ten times the goodput.

package pathctl

import (
	"math"
	"time"
)

const (
	// ctlTick is how often the controller looks. KCP's own update loop wakes
	// every 10-40 ms, and the smoothed RTT moves no faster than the acks do.
	ctlTick = 25 * time.Millisecond

	// rttTau is the time constant of the smoothing the RTT is read through.
	rttTau = 400 * time.Millisecond

	// stepEvery is the time between two changes of the window once slow start
	// is over. A change takes about as long as the smoothing to show in the
	// readings, so changing sooner is changing blind.
	stepEvery = 500 * time.Millisecond

	// cutWait is the extra pause after a cut.
	cutWait = 1500 * time.Millisecond

	// txErrsCut is how many refused segments, and txErrsPerMille what share of
	// the segments sent in the same span, count as an overflow. A burst that
	// overflows the local queue for an instant (a window's worth leaving at once
	// into a queue of 1000) refuses a few segments in thousands and costs KCP
	// nothing it cannot repair; it is a sender that stays over the queue's drain
	// rate that refuses one in a hundred. Counting the first as the second cut
	// the window of a path with no queue and no limit at every step, and the
	// soak's 25 ms, 0.5% loss path lost a quarter of its speed to it (122
	// Mbit/s with 16 streams where the same build with the controller off,
	// or with the share, made 168).
	txErrsCut      = 3
	txErrsPerMille = 10
	overflowSpan   = 100 * time.Millisecond

	// probeEvery and probeFor: see the comment above.
	probeEvery = 15 * time.Second
	probeFor   = 300 * time.Millisecond

	// baseWindow is how far back the lowest RTT is remembered. Long enough to
	// see the path empty between bulk transfers, short enough for a route
	// change to be believed.
	baseWindow = 20 * time.Second

	// slackMin and slackFrac set the queue the controller leaves on the path:
	// max(slackMin, base/slackFrac), which is 25 ms at the 75 ms of the soak
	// path. The floor covers the RTT's own noise (jitter, a scheduler tick, the
	// ack batching of the slower modes) and keeps it from reading a quiet LAN's
	// microsecond wobble as congestion; the fraction lets a long path hold a
	// longer queue, as it has a longer round trip to fill. Smaller values were
	// tried: the window then follows the noise, and the throughput of bulk
	// transfers beside many short connections fell by a quarter.
	slackMin  = 25 * time.Millisecond
	slackFrac = 3

	// paceGain is how far over window/RTT the sessions' output may run. KCP
	// puts out what the acknowledgements free in one go, and the ack batching
	// of the slower modes turns a steady flow into a burst per tick: a
	// window's worth of packets leave in a few microseconds, into a bottleneck
	// that holds 1000 of them. Spread over the round trip they arrive as the
	// stream they are. The pace is a ceiling on the burst, not the rate: the
	// window is what sets the rate, and a window under the path's capacity is
	// sent at the speed the acknowledgements allow, which is under the pace.
	paceGain = 1.5
	// paceSpanMul and paceSpanAdd bound the round trip the pace is computed
	// over: see paceRate.
	paceSpanMul = 4
	paceSpanAdd = 100 * time.Millisecond

	// marginSlacks is how many slacks the retransmission timer waits beyond
	// the smoothed round trip. The window control lets the round trip stretch
	// by one slack before it calls it a queue; the timer needs that much to
	// stay quiet, and as much again for the variance KCP's estimator (which
	// forgets quickly) has lost by the time the queue grows. Measured on the
	// soak path with the control and the pace on: with no margin 8% of
	// segments were sent by the timer and 7.5% arrived twice, at
	// one slack and a little over (40 ms on a 75 ms path) 1.3%, at two 0.3%.
	marginSlacks = 2
	// marginStep is the least change of the margin worth the session's lock.
	marginStep = 5 * time.Millisecond

	// minWindow and startWindow: a session never goes under a window that can
	// carry a few streams' first flights (or a sixty-fourth of its ceiling, if
	// that is more), and never starts so low that slow start takes more than a
	// few round trips to reach a useful size.
	// quietAfter is how long a session may receive nothing and still have a say
	// in the reverse queue, when sessions may abstain.
	quietAfter = 500 * time.Millisecond

	minWindow   = 32
	startWindow = 64
)

// ctlInput is what the controller read in one tick.
type ctlInput struct {
	now time.Time
	dt  time.Duration
	// sample is false while KCP has no RTT to give (it keeps whole
	// milliseconds, so a LAN reads 0 as well): no evidence of a queue.
	sample bool
	// srtt is the worst session's KCP RTT, cur its smoothed one, base the
	// path's lowest.
	srtt, cur, base time.Duration
	// rev is the queue the data we receive has met on its way to us. It is not
	// in srtt, cur and base, which are of our direction only (pathReader.read).
	rev time.Duration
	// tx is the running count of segments the local queue refused, and sent
	// the running count of those it took.
	tx, sent uint64
	// probing is true while the window is being halved to empty the queue.
	probing bool
}

// ctlState is the controller's decision state, apart from what it reads.
type ctlState struct {
	floor, ceiling float64
	w              float64
	slow           bool
	lastStep       time.Time
	// txBefore and sentBefore are the counts at the start of the span the
	// overflow share is measured over.
	txBefore, sentBefore uint64
	spanStart            time.Time
	cuts                 uint64
	// revTolerant widens the slack by half the queue of the way back; see
	// Config.RevTolerant.
	revTolerant bool
}

// overflow reports whether the local queue has refused a noticeable share of
// what was sent since the span began. In slow start the span is cut short and
// restarted every overflowSpan, so that a thousand segments sent clean do not
// hide a burst of refusals at the end of it; in the steady state it is the
// step, and the caller restarts it.
func (s *ctlState) overflow(in ctlInput) bool {
	tx := in.tx - s.txBefore
	if tx < txErrsCut {
		return false
	}
	sent := in.sent - s.sentBefore
	return tx*1000 >= (sent+tx)*txErrsPerMille
}

// restartSpan begins a new span of overflow and loss counting at in.
func (s *ctlState) restartSpan(in ctlInput) {
	s.txBefore, s.sentBefore, s.spanStart = in.tx, in.sent, in.now
}

func newCtlState(ceiling int) *ctlState {
	c := float64(ceiling)
	fl := min(max(minWindow, c/64), c)
	return &ctlState{floor: fl, ceiling: c, w: min(max(startWindow, fl), c), slow: true}
}

// decide takes a reading and returns the window the sessions should have.
func (s *ctlState) decide(in ctlInput) float64 {
	slack := slackFor(in.base)
	if s.revTolerant {
		slack += in.rev / 2
	}
	rttSpan := max(in.srtt, 100*time.Millisecond)
	errs := s.overflow(in)
	if s.slow && !errs && in.now.Sub(s.spanStart) >= overflowSpan {
		s.restartSpan(in)
	}
	switch {
	case in.probing:
		// Hold: the readings are of a queue being emptied on purpose.
	case s.slow:
		// Double per RTT, until the first sign of a queue.
		if (in.sample && in.srtt > in.base+slack) || errs {
			s.w = max(s.floor, s.w*0.8)
			s.slow = false
			s.lastStep = in.now.Add(cutWait)
			s.restartSpan(in)
			s.cuts++
		} else {
			s.w += s.w * float64(in.dt) / float64(rttSpan)
		}
	case in.now.Sub(s.lastStep) < stepEvery:
		// Hold: what the last change did has not reached the readings.
	default:
		s.lastStep = in.now
		excess := in.cur - in.base
		f := 1.0
		if in.sample && excess > slack {
			f = min(0.95, max(0.75, float64(in.base+slack)/float64(in.cur)))
		}
		if errs {
			f = min(f, 0.9)
		}
		switch {
		case f < 1:
			s.w *= f
			s.cuts++
			// What was sent before the cut is still arriving, as a queue and
			// as errors: wait it out before reading again.
			s.lastStep = in.now.Add(cutWait)
		case in.sample && excess < slack/2 && !errs:
			s.w += max(2, s.w*0.08)
		}
		s.restartSpan(in)
	}
	s.w = min(s.ceiling, max(s.floor, s.w))
	return s.w
}

// batchingFor says whether acknowledgements and small writes should be batched
// on a path whose lowest round trip is base, with a KCP tick of tick. Batching
// holds an acknowledgement for up to a tick, so where the round trip is shorter
// than that (a LAN, a veth pair, a short hop) the tick IS the round trip, and a
// single stream's speed is its window over it: Turbo's one stream on a clean
// veth pair fell from 640 to 390 Mbit/s. Where the round trip is longer the
// wait is a fraction of it, and what batching saves — half the packets, which
// is what a bottleneck queue counts — is worth more. Below half a tick batching
// is off, above a whole tick it is on, and in between the last choice stands;
// a path with no RTT reading yet (KCP keeps whole milliseconds, and 0 is a
// LAN) is a short one.
func batchingFor(prev bool, sample bool, base, tick time.Duration) bool {
	switch {
	case !sample:
		return false
	case base <= tick/2:
		return false
	case base >= tick:
		return true
	}
	return prev
}

// slackFor is the queue the control lets the round trip stretch by on a path
// whose lowest round trip is base.
func slackFor(base time.Duration) time.Duration { return max(slackMin, base/slackFrac) }

// retransmitMargin is how long the sessions' retransmission timers wait beyond
// what KCP computes, given what the control reads: marginSlacks of the slack it
// lets the round trip stretch by. A path with no round-trip reading (a LAN) has
// none, and KCP's own timer stands.
func retransmitMargin(in ctlInput) time.Duration {
	if !in.sample {
		return 0
	}
	return marginSlacks*slackFor(in.base) + in.rev
}

// paceRate is the byte rate the sessions' output is held to, or 0 for none: the
// window's worth of packets of mtu bytes over the round trip, paceGain times.
// A path with no round-trip reading is one too short to need it.
func paceRate(in ctlInput, window int64, mtu int) uint32 {
	if !in.sample || in.cur <= 0 {
		return 0
	}
	// The round trip is what the worst session reads, and one session reading
	// seconds (a retransmission timer that fired late, a stall of its socket)
	// must not set the pace of all of them: the pace is applied to the packets
	// after they are stamped, so a pace slowed by a long round trip adds to the
	// delay the receiver reads, which lengthens the round trip, which slows the
	// pace. Seen once in a 150 s run at 10% loss: one session read 8 s and the
	// tunnel carried nothing for 27 s, with every session's timer at its limit.
	// A round trip over paceSpanMul times the lowest plus paceSpanAdd is not
	// read as longer for this purpose; the window is what limits the rate then.
	cur := in.cur
	if in.base > 0 {
		cur = min(cur, paceSpanMul*in.base+paceSpanAdd)
	}
	r := paceGain * float64(window) * float64(mtu) / cur.Seconds()
	if r >= math.MaxUint32 {
		return math.MaxUint32
	}
	return max(1, uint32(r))
}

// delayFilter is one session's view of a delay, the round trip's or the one-way
// delay of the data it receives: cur is the smoothed reading and base the
// lowest cur of the last baseWindow, kept as one-second buckets. The one-way
// delay is relative to a clock that is not ours, so its zero means nothing and
// "no reading" is a flag, not a zero.
type delayFilter struct {
	buckets [int(baseWindow / time.Second)]struct {
		v  time.Duration
		ok bool
	}
	bucket      int
	bucketStart time.Time
	have        bool
	cur, base   time.Duration
	// lastData is when a reading last came from data that arrived.
	lastData time.Time
}

// lowest records a reading taken while the queue was drained as a floor for base.
func (f *delayFilter) lowest(v time.Duration) {
	if b := &f.buckets[f.bucket]; !b.ok || v < b.v {
		b.v, b.ok = v, true
	}
}

// add takes a reading, dt after the last.
func (f *delayFilter) add(now time.Time, dt, v time.Duration) {
	if !f.have {
		f.cur, f.have = v, true
	} else {
		a := min(1, float64(dt)/float64(rttTau))
		f.cur += time.Duration(a * float64(v-f.cur))
	}
	if f.bucketStart.IsZero() {
		f.bucketStart = now
	}
	if now.Sub(f.bucketStart) >= time.Second {
		f.bucket = (f.bucket + 1) % len(f.buckets)
		f.buckets[f.bucket].ok = false
		f.bucketStart = now
	}
	f.lowest(f.cur)
	first := true
	for _, b := range f.buckets {
		if b.ok && (first || b.v < f.base) {
			f.base, first = b.v, false
		}
	}
}

// queue is how far the smoothed reading stands above the lowest lately.
func (f *delayFilter) queue() time.Duration { return max(0, f.cur-f.base) }

// delaySource is what the controller reads of a session.
type delaySource interface {
	// GetSRTT is KCP's smoothed round trip in milliseconds, 0 for none.
	GetSRTT() int32
	// TakeRecvDelay is the lowest one-way reading of the data received since
	// the last call (see kcp-go's sess.go).
	TakeRecvDelay() (ms int64, ok bool)
}

// pathReader reads the path from the sessions: one delayFilter per session for
// the round trip and one for the delay of the data it receives.
type pathReader struct {
	rtt, owd map[delaySource]*delayFilter
	// abstain leaves a session that has received nothing for a while out of the
	// reverse queue's vote; see Config.QuietAbstain.
	abstain bool
}

func newPathReader() *pathReader {
	return &pathReader{rtt: map[delaySource]*delayFilter{}, owd: map[delaySource]*delayFilter{}}
}

// read fills in the delay fields of in from what the sessions ks report. All of
// them are of our own direction: the round trip less the queue the data we
// receive has met.
//
// The reverse direction is read first. A round trip is the queue of the way
// there and of the way back, and the window control of each end moves only the
// way there. With the sum as its signal, the end whose direction was the less
// loaded read the other's queue as its own, and cut for it: one direction of
// the soak path ran at 45 Mbit/s and the other at 6, for a minute at a time,
// and swapped. The way back is measured where the data arrives, from the
// timestamp KCP puts in every data segment: the clocks differ by a constant, so
// one reading is nothing, and the rise of it over the lowest lately is the
// queue (the filter forgets the lowest after baseWindow, which is also what
// keeps a drifting clock from adding up to anything). Nothing else is needed
// from the peer, and nothing on the wire changes.
func (p *pathReader) read(in *ctlInput, ks []delaySource) {
	live := make(map[delaySource]bool, len(ks))
	for _, kc := range ks {
		live[kc] = true
		o := p.owd[kc]
		if o == nil {
			o = &delayFilter{}
			p.owd[kc] = o
		}
		v, ok := kc.TakeRecvDelay()
		d := time.Duration(v) * time.Millisecond
		switch {
		case ok:
		case o.have:
			// Nothing arrived since the last look, so nothing is queued in
			// that direction that we could see: the reading falls to its floor.
			d = o.base
		default:
			continue
		}
		o.add(in.now, in.dt, d)
		if ok {
			o.lastData = in.now
		}
		if in.probing {
			o.lowest(d)
		}
	}
	// The sessions share the path: the floor of the one-way delay is the lowest
	// any of them has seen, so that a session that never saw it empty is not
	// read as having a queue, and the queue is the lowest any of them reads, so
	// that one with a high reading of its own does not hide ours.
	var floor time.Duration
	haveFloor := false
	for _, o := range p.owd {
		if o.have && (!haveFloor || o.base < floor) {
			floor, haveFloor = o.base, true
		}
	}
	haveRev := false
	for _, o := range p.owd {
		if p.abstain && in.now.Sub(o.lastData) > quietAfter {
			continue
		}
		if q := max(0, o.cur-floor); o.have && (!haveRev || q < in.rev) {
			in.rev, haveRev = q, true
		}
	}

	haveBase := false
	for _, kc := range ks {
		f := p.rtt[kc]
		if f == nil {
			f = &delayFilter{}
			p.rtt[kc] = f
		}
		// KCP keeps its smoothed RTT in whole milliseconds, and 0 means
		// both "no sample yet" and "under a millisecond": a LAN. Neither
		// is evidence of a queue, so neither cuts the window and neither
		// counts towards the base.
		ms := kc.GetSRTT()
		if ms <= 0 {
			continue
		}
		d := max(time.Duration(ms)*time.Millisecond-in.rev, time.Millisecond)
		f.add(in.now, in.dt, d)
		if in.probing {
			f.lowest(d)
		}
		in.sample = true
		// The sessions share a path: its base is the lowest any of them
		// has seen, and a queue is what the worst sees.
		if !haveBase || f.base < in.base {
			in.base, haveBase = f.base, true
		}
		in.cur = max(in.cur, f.cur)
		in.srtt = max(in.srtt, d)
	}
	for kc := range p.rtt {
		if !live[kc] {
			delete(p.rtt, kc)
			delete(p.owd, kc)
		}
	}
}

// applied is what the controller last set on one session, so a tick that
// changes nothing costs the session's lock nothing.
type applied struct {
	margin time.Duration
	rate   uint32
	// batching is the state of the ack batching the session has been given.
	batching bool
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
