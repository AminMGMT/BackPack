package pathctl

import (
	"math"
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// in builds a reading at second t of a run.
func reading(t0 time.Time, at time.Duration, srtt, cur, base time.Duration, tx uint64) ctlInput {
	return ctlInput{now: t0.Add(at), dt: ctlTick, sample: base > 0, srtt: srtt, cur: cur, base: base, tx: tx}
}

func TestTheWindowDoublesPerRTTUntilItSeesAQueue(t *testing.T) {
	s := newCtlState(1024)
	t0 := time.Unix(0, 0)
	w0 := s.w
	if w0 != 64 {
		t.Fatalf("starts at %v", w0)
	}
	// 75 ms round trip, no queue: the window about doubles in an RTT (3 ticks).
	for i := 0; i < 3; i++ {
		s.decide(reading(t0, time.Duration(i)*ctlTick, ms(75), ms(75), ms(75), 0))
	}
	if s.w < 1.6*w0 || s.w > 2.4*w0 {
		t.Fatalf("after one RTT the window is %v from %v", s.w, w0)
	}
	if !s.slow {
		t.Fatal("slow start ended with no sign of a queue")
	}
	// A queue (srtt over base+slack) ends slow start and cuts.
	before := s.w
	s.decide(reading(t0, time.Second, ms(75+60), ms(75), ms(75), 0))
	if s.slow || s.w >= before {
		t.Fatalf("slow=%v window %v -> %v after a queue was seen", s.slow, before, s.w)
	}
	if s.cuts != 1 {
		t.Fatalf("%d cuts", s.cuts)
	}
}

func TestAFlatLANGrowsToTheCeilingAndStays(t *testing.T) {
	s := newCtlState(1024)
	t0 := time.Unix(0, 0)
	// KCP reads 0 ms: no sample, so no evidence of a queue.
	for i := 0; i < 2000; i++ {
		in := ctlInput{now: t0.Add(time.Duration(i) * ctlTick), dt: ctlTick}
		s.decide(in)
	}
	if s.w != 1024 {
		t.Fatalf("window %v on a LAN, want the ceiling 1024", s.w)
	}
}

func TestAQueueThatStaysIsCutOncePerLookNotOnEveryTick(t *testing.T) {
	s := newCtlState(1024)
	s.slow = false
	s.w = 600
	t0 := time.Unix(0, 0)
	// A standing 40 ms over base+slack: cut, then not again until cutWait and
	// stepEvery have passed, however many ticks go by.
	for i := 0; i < int((cutWait+stepEvery)/ctlTick)-2; i++ {
		s.decide(reading(t0, time.Duration(i)*ctlTick, ms(150), ms(150), ms(75), 0))
	}
	if s.cuts != 1 {
		t.Fatalf("%d cuts in %s, want 1", s.cuts, cutWait+stepEvery)
	}
	if s.w >= 600 || s.w < 0.7*600 {
		t.Fatalf("the cut left %v from 600", s.w)
	}
	// It keeps cutting while the queue stays, but never under the floor.
	for i := 0; i < 100000; i++ {
		s.decide(reading(t0, time.Second+time.Duration(i)*ctlTick, ms(300), ms(300), ms(75), 0))
	}
	if s.w != s.floor {
		t.Fatalf("a queue that never left drove the window to %v, floor %v", s.w, s.floor)
	}
}

func TestOverflowErrorsCutTheWindowWithoutAnyDelaySignal(t *testing.T) {
	s := newCtlState(1024)
	s.slow = false
	s.w = 600
	t0 := time.Unix(0, 0)
	s.decide(reading(t0, 0, ms(75), ms(75), ms(75), 0))
	s.decide(reading(t0, time.Second, ms(75), ms(75), ms(75), 2)) // below the threshold
	if s.cuts != 0 {
		t.Fatal("two refused segments cut the window")
	}
	before := s.w
	s.decide(reading(t0, 2*time.Second, ms(75), ms(75), ms(75), 40))
	if s.cuts != 1 || s.w >= before {
		t.Fatalf("cuts=%d window %v -> %v after 40 refused segments", s.cuts, before, s.w)
	}
}

func TestAClearPathGrowsTheWindowAndAMiddlingOneLeavesIt(t *testing.T) {
	s := newCtlState(1024)
	s.slow = false
	s.w = 300
	t0 := time.Unix(0, 0)
	base := ms(75)
	s.decide(reading(t0, 0, base, base+ms(5), base, 0))
	if s.w <= 300 {
		t.Fatalf("window %v on a clear path", s.w)
	}
	// Between half the slack and the slack: hold.
	w := s.w
	s.decide(reading(t0, time.Second, base, base+slackMin*3/4, base, 0))
	if s.w != w {
		t.Fatalf("window moved %v -> %v inside the band", w, s.w)
	}
}

func TestProbingHoldsTheWindowAndLossAloneDoesNotMoveIt(t *testing.T) {
	s := newCtlState(1024)
	s.slow = false
	s.w = 500
	t0 := time.Unix(0, 0)
	in := reading(t0, 0, ms(300), ms(300), ms(75), 0)
	in.probing = true
	s.decide(in)
	if s.w != 500 || s.cuts != 0 {
		t.Fatalf("a probing tick changed the window to %v", s.w)
	}
	// Loss never reaches the controller at all: the same delays with no
	// queue are a clear path, whatever the packets did on the way.
	s.decide(reading(t0, time.Second, ms(75), ms(75), ms(75), 0))
	if s.w < 500 {
		t.Fatalf("window fell to %v on a path with no queue", s.w)
	}
}

func TestTheFloorAndStartScaleWithTheCeilingButStayUsable(t *testing.T) {
	for _, c := range []struct{ ceil, floor, start float64 }{
		{50, 32, 50}, {256, 32, 64}, {1024, 32, 64}, {4096, 64, 64}, {32768, 512, 512},
	} {
		s := newCtlState(int(c.ceil))
		if s.floor != c.floor || s.w != c.start {
			t.Errorf("ceiling %v: floor %v start %v, want %v and %v", c.ceil, s.floor, s.w, c.floor, c.start)
		}
	}
}

func TestTheBaseRTTFollowsTheLowestSmoothedReadingAndForgets(t *testing.T) {
	var f delayFilter
	t0 := time.Unix(0, 0)
	now := t0
	for i := 0; i < 80; i++ { // 2 s at 75 ms
		now = now.Add(ctlTick)
		f.add(now, ctlTick, ms(75))
	}
	if f.base != ms(75) {
		t.Fatalf("base %v", f.base)
	}
	// The path gets longer for good: the base follows once the old low reading
	// has left the window.
	for i := 0; i < int(baseWindow/ctlTick)+200; i++ {
		now = now.Add(ctlTick)
		f.add(now, ctlTick, ms(150))
	}
	if f.base < ms(140) {
		t.Fatalf("base still %v after the path moved to 150 ms", f.base)
	}
}

func TestBatchingIsOffWhereTheRoundTripIsShorterThanTheTick(t *testing.T) {
	tick := 20 * time.Millisecond
	for _, c := range []struct {
		name   string
		prev   bool
		sample bool
		base   time.Duration
		want   bool
	}{
		{"no reading yet is a short path", true, false, 0, false},
		{"a LAN", true, true, ms(1), false},
		{"half a tick", true, true, ms(10), false},
		{"between, it stays on", true, true, ms(15), true},
		{"between, it stays off", false, true, ms(15), false},
		{"a whole tick", false, true, ms(20), true},
		{"a WAN", false, true, ms(75), true},
	} {
		if got := batchingFor(c.prev, c.sample, c.base, tick); got != c.want {
			t.Errorf("%s: batching %v, want %v", c.name, got, c.want)
		}
	}
}

// A burst that overflows the local queue for an instant refuses a few segments
// in thousands; only a sender that stays over the queue's drain rate refuses a
// hundredth of what it sends. The first is KCP's to repair, the second is a
// reason to send less.
func TestOnlyAShareOfRefusalsCountsAsAnOverflow(t *testing.T) {
	s := newCtlState(1024)
	s.slow = false
	s.w = 600
	t0 := time.Unix(0, 0)
	s.decide(reading(t0, 0, ms(75), ms(75), ms(75), 0))
	in := reading(t0, time.Second, ms(75), ms(75), ms(75), 40)
	in.sent = 20000 // 0.2%
	s.decide(in)
	if s.cuts != 0 {
		t.Fatalf("40 refusals in 20000 segments cut the window (%d cuts)", s.cuts)
	}
	in = reading(t0, 3*time.Second, ms(75), ms(75), ms(75), 40+300)
	in.sent = 20000 + 20000 // 300 of 20000 more: 1.5%
	s.decide(in)
	if s.cuts != 1 {
		t.Fatalf("1.5%% of the segments refused and the window was not cut (%d cuts)", s.cuts)
	}
}

func TestSlowStartIgnoresAFewRefusalsInAFlood(t *testing.T) {
	s := newCtlState(1024)
	t0 := time.Unix(0, 0)
	var tx, sent uint64
	for i := 0; i < 200; i++ { // 5 s, a refusal in every 500 segments
		in := reading(t0, time.Duration(i)*ctlTick, ms(75), ms(75), ms(75), 0)
		sent += 500
		tx++
		in.tx, in.sent = tx, sent
		s.decide(in)
	}
	if !s.slow || s.cuts != 0 {
		t.Fatalf("slow=%v cuts=%d after refusals of 0.2%% of what was sent", s.slow, s.cuts)
	}
}

func TestThePaceFollowsTheWindowAndStaysOffWithoutARoundTrip(t *testing.T) {
	if r := paceRate(ctlInput{}, 500, 1350); r != 0 {
		t.Fatalf("pace %d with no round trip reading", r)
	}
	in := ctlInput{sample: true, cur: ms(100)}
	// 500 packets of 1350 bytes over 100 ms, a half over.
	want := uint32(paceGain * 500 * 1350 / 0.1)
	if r := paceRate(in, 500, 1350); r != want {
		t.Fatalf("pace %d, want %d", r, want)
	}
	if r := paceRate(in, 1<<40, 1350); r != math.MaxUint32 {
		t.Fatalf("pace %d on an absurd window", r)
	}
}

// One session reading seconds must not slow every session's pace to a crawl:
// the pace is applied after the packets are stamped, so a slow pace is itself
// read as a longer round trip.
func TestOneSessionReadingSecondsDoesNotThrottleThePace(t *testing.T) {
	in := ctlInput{sample: true, base: ms(70), cur: ms(8000)}
	got := paceRate(in, 36, 1198)
	span := 0.070*paceSpanMul + 0.1
	want := uint32(paceGain * 36 * 1198 / span)
	if got != want {
		t.Fatalf("pace %d on a round trip of 8 s over a base of 70 ms, want %d", got, want)
	}
	// Below the bound the reading is used as it is.
	in.cur = ms(200)
	rtt := 0.2
	if got, want := paceRate(in, 36, 1198), uint32(paceGain*36*1198/rtt); got != want {
		t.Fatalf("pace %d, want %d", got, want)
	}
}

func TestTheRetransmitMarginIsTheQueueTheControlAllowsPlusTheReverseOne(t *testing.T) {
	if m := retransmitMargin(ctlInput{}); m != 0 {
		t.Fatalf("margin %v with no reading", m)
	}
	in := ctlInput{sample: true, base: ms(75)}
	if m := retransmitMargin(in); m != marginSlacks*slackMin {
		t.Fatalf("margin %v at 75 ms, want %v", m, marginSlacks*slackMin)
	}
	in.base = ms(300) // a long path holds a longer queue: base/3 = 100 ms
	if m := retransmitMargin(in); m != marginSlacks*ms(100) {
		t.Fatalf("margin %v at 300 ms", m)
	}
	in.rev = ms(30)
	if m := retransmitMargin(in); m != marginSlacks*ms(100)+ms(30) {
		t.Fatalf("margin %v with 30 ms of reverse queue", m)
	}
}

func TestTheDelayFilterTakesNegativeAndZeroReadings(t *testing.T) {
	// A one-way delay is read against a clock that is not ours, so its values
	// may be anything, and zero is a reading.
	var f delayFilter
	now := time.Unix(0, 0)
	for _, v := range []time.Duration{-ms(5000), -ms(5000), -ms(4990), -ms(5000)} {
		now = now.Add(ctlTick)
		f.add(now, ctlTick, v)
	}
	if !f.have || f.base > -ms(4999) || f.queue() > ms(10) {
		t.Fatalf("base %v cur %v queue %v", f.base, f.cur, f.queue())
	}
	var z delayFilter
	z.add(now, ctlTick, 0)
	if !z.have || z.base != 0 {
		t.Fatalf("a zero reading was not kept: have=%v base=%v", z.have, z.base)
	}
}

type fakeSession struct {
	srtt int32
	owd  int64
	ok   bool
}

func (f *fakeSession) GetSRTT() int32               { return f.srtt }
func (f *fakeSession) TakeRecvDelay() (int64, bool) { return f.owd, f.ok }

// The round trip is the queue of the way there and of the way back. The control
// moves the way there, so it must read that one: a queue the data we receive
// met is the other end's to cut, and left in the reading it made the less
// loaded direction of the soak path give way for the more loaded one.
func TestTheControlReadsItsOwnDirectionNotTheRoundTrip(t *testing.T) {
	p := newPathReader()
	a, b := &fakeSession{srtt: 75, ok: true}, &fakeSession{srtt: 75, ok: true}
	ss := []delaySource{a, b}
	t0 := time.Unix(0, 0)
	tick := 0
	run := func(n int) (in ctlInput) {
		for i := 0; i < n; i++ {
			tick++
			in = ctlInput{now: t0.Add(time.Duration(tick) * ctlTick), dt: ctlTick}
			p.read(&in, ss)
		}
		return in
	}
	// The peer's clock is 7 hours ahead; nothing is queued.
	a.owd, b.owd = -25200000, -25200000
	in := run(80)
	if in.rev != 0 || in.base != ms(75) {
		t.Fatalf("idle path: rev %v base %v", in.rev, in.base)
	}
	// The way back fills: the round trip and the data's one-way delay both
	// rise by 60 ms. The reading of our own way stays where it was.
	a.srtt, b.srtt = 135, 135
	a.owd, b.owd = -25200000+60, -25200000+60
	in = run(80)
	if in.rev < ms(55) || in.rev > ms(60) {
		t.Fatalf("reverse queue read as %v, want about 60 ms", in.rev)
	}
	if in.cur-in.base > ms(10) || in.srtt-in.base > ms(10) {
		t.Fatalf("a reverse queue left %v / %v over the base in our own direction's reading", in.cur-in.base, in.srtt-in.base)
	}
	// Ours fills as well: the round trip rises, the arriving data's delay does not.
	a.srtt, b.srtt = 195, 195
	in = run(80)
	if in.cur-in.base < ms(50) {
		t.Fatalf("our own queue read as %v", in.cur-in.base)
	}
	// One session that received nothing, and one with a reading of its own,
	// do not make a reverse queue.
	b.ok = false
	a.owd = -25200000 + 400
	in = run(80)
	if in.rev > ms(10) {
		t.Fatalf("a quiet session did not hold the reverse reading down: rev %v", in.rev)
	}
}

type busySession struct {
	fakeSession
	backlog int
}

func (b *busySession) Backlog() int { return b.backlog }

func (b *busySession) SetWindowSize(int, int)     {}
func (b *busySession) SetRTOMargin(time.Duration) {}
func (b *busySession) SetRateLimit(uint32)        {}
func (b *busySession) SetACKNoDelay(bool)         {}
func (b *busySession) SetWriteDelay(bool)         {}

// The window the control settles at is what the sessions in use have between
// them. Eight sessions of which two carry the transfer have the budget of eight
// divided by two, and a session that stops being busy stops counting only after
// a moment, so a gap between two bursts does not make the next one start small.
func TestTheWindowIsDividedAmongTheBusySessions(t *testing.T) {
	ss := make([]Session, 8)
	bs := make([]*busySession, 8)
	for i := range ss {
		bs[i] = &busySession{}
		ss[i] = bs[i]
	}
	busy := map[Session]time.Time{}
	t0 := time.Unix(100, 0)

	if got := split(100, ss, busy, t0, 32, 1024); got != 800 {
		t.Fatalf("nobody busy: %d, want the whole budget to one session (800)", got)
	}
	bs[0].backlog, bs[1].backlog = 10, 3
	if got := split(100, ss, busy, t0, 32, 1024); got != 400 {
		t.Fatalf("two busy: %d, want 400 each", got)
	}
	for _, b := range bs {
		b.backlog = 5
	}
	if got := split(100, ss, busy, t0, 32, 1024); got != 100 {
		t.Fatalf("all busy: %d, want the window itself, 100", got)
	}
	// Quiet again: they still count for busyFor, then do not.
	for _, b := range bs {
		b.backlog = 0
	}
	if got := split(100, ss, busy, t0.Add(busyFor/2), 32, 1024); got != 100 {
		t.Fatalf("just quiet: %d, want 100 while they still count as busy", got)
	}
	if got := split(100, ss, busy, t0.Add(2*busyFor), 32, 1024); got != 800 {
		t.Fatalf("long quiet: %d, want 800", got)
	}
	// The preset's window is the ceiling and the floor holds.
	bs[0].backlog = 1
	if got := split(300, ss, busy, t0.Add(3*busyFor), 32, 1024); got != 1024 {
		t.Fatalf("over the ceiling: %d, want 1024", got)
	}
	if got := split(1, ss, busy, t0.Add(3*busyFor), 32, 1024); got != 32 {
		t.Fatalf("under the floor: %d, want 32", got)
	}
	// A session that is gone is forgotten.
	split(100, ss[:2], busy, t0.Add(3*busyFor), 32, 1024)
	if len(busy) > 2 {
		t.Fatalf("%d sessions remembered, 2 are up", len(busy))
	}
}

// A peer that does not run the control lets its own direction's queue swing by
// hundreds of milliseconds, and the round trip less that queue is as noisy.
// Where the control is told to expect that it tolerates half the queue of the
// way back on top of its slack; with a quiet way back it is the same control.
func TestRevToleranceWidensTheSlackByHalfTheQueueOfTheWayBack(t *testing.T) {
	t0 := time.Unix(0, 0)
	cut := func(tolerant bool, rev time.Duration) bool {
		s := newCtlState(1024)
		s.slow = false
		s.w = 400
		s.revTolerant = tolerant
		// Our own direction reads 60 ms over the base: over the 25 ms slack.
		in := ctlInput{now: t0.Add(time.Hour), dt: ctlTick, sample: true,
			srtt: ms(135), cur: ms(135), base: ms(75), rev: rev}
		s.lastStep = t0
		before := s.w
		s.decide(in)
		return s.w < before
	}
	if !cut(false, ms(400)) || !cut(true, 0) || !cut(true, ms(20)) {
		t.Fatal("a queue of our own went uncut where the tolerance does not reach it")
	}
	if cut(true, ms(400)) {
		t.Fatal("60 ms over the base was cut with 400 ms of queue on the way back and the tolerance on")
	}
}
