package network

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/pathctl"
)

// fakePath is a session as the controller sees it, recording what it is told.
type fakePath struct {
	mu                  sync.Mutex
	snd, rcv            int
	margin              time.Duration
	rate                uint32
	ack, wdelay         bool
	srtt                int32
	windowSets, ackSets int
}

func (f *fakePath) GetSRTT() int32               { return f.srtt }
func (f *fakePath) TakeRecvDelay() (int64, bool) { return 0, false }
func (f *fakePath) SetRTOMargin(d time.Duration) { f.mu.Lock(); f.margin = d; f.mu.Unlock() }
func (f *fakePath) SetRateLimit(r uint32)        { f.mu.Lock(); f.rate = r; f.mu.Unlock() }
func (f *fakePath) SetWriteDelay(d bool)         { f.mu.Lock(); f.wdelay = d; f.mu.Unlock() }
func (f *fakePath) SetWindowSize(snd, rcv int) {
	f.mu.Lock()
	f.snd, f.rcv = snd, rcv
	f.windowSets++
	f.mu.Unlock()
}
func (f *fakePath) SetACKNoDelay(v bool) { f.mu.Lock(); f.ack = v; f.ackSets++; f.mu.Unlock() }
func (f *fakePath) window() int          { f.mu.Lock(); defer f.mu.Unlock(); return f.snd }

// fakeCarrier is a socket that counts what its transmit queue refused and took.
type fakeCarrier struct{ refused, sent atomic.Uint64 }

func (c *fakeCarrier) TxStats() (uint64, uint64) { return c.refused.Load(), c.sent.Load() }

func pckSettings() KCPSettings {
	return KCPSettings{MTU: 1250, Interval: 10, SndWnd: 1024, RcvWnd: 1024, Pck: &PcapCarrier{}}
}

// The opt-in: a settings value gets a path control only when something asks
// for one, and it is sized from the preset's numbers, not defaults of its own.
func TestPathControlIsOffUntilAskedForAndSizedFromTheSettings(t *testing.T) {
	s := pckSettings()
	if s.Path != nil {
		t.Fatal("a settings value has a path control before EnablePathControl")
	}
	EnablePathControl(&s)
	p := s.Path
	if p == nil {
		t.Fatal("EnablePathControl left Path nil")
	}
	if p.Ceiling() != 1024 || p.rcv != 1024 {
		t.Fatalf("window %d/%d, want the preset's 1024/1024", p.Ceiling(), p.rcv)
	}
	if want := 1250 - pckOverhead; p.mtu != want {
		t.Fatalf("pace MTU %d, want the effective %d (the pck framing is not payload)", p.mtu, want)
	}
	if p.tick != 10*time.Millisecond {
		t.Fatalf("tick %v, want the preset's 10 ms", p.tick)
	}
	if p.Window() != 0 {
		t.Fatalf("window %d with no session under control, want 0", p.Window())
	}
}

// A session that is put under control takes the controller's window at once
// and not the preset's, and the controller exists only while there is a session.
func TestPathControlRunsWhileThereIsASessionAndStartsOver(t *testing.T) {
	s := pckSettings()
	EnablePathControl(&s)
	p := s.Path

	a := &fakePath{}
	p.add(a, nil)
	if a.window() <= 0 || a.window() >= 1024 {
		t.Fatalf("a new session got window %d, want the controller's start (under the preset's 1024)", a.window())
	}
	if a.rcv != 1024 {
		t.Fatalf("the receive window was set to %d, want the preset's 1024", a.rcv)
	}
	first := p.ctl
	if first == nil {
		t.Fatal("no controller with a session under control")
	}

	// A second session joins the one that is running.
	b := &fakePath{}
	p.add(b, nil)
	p.add(b, nil) // twice is once
	if p.ctl != first || len(p.members) != 2 {
		t.Fatalf("controller replaced or session counted twice: %d members", len(p.members))
	}

	p.remove(a)
	if p.ctl == nil {
		t.Fatal("the controller stopped while a session was left")
	}
	p.remove(b)
	p.remove(b) // twice is once
	if p.ctl != nil || p.Window() != 0 {
		t.Fatal("the controller kept running with no session")
	}

	// The pool comes back: a new run, from slow start.
	c := &fakePath{}
	p.add(c, nil)
	defer p.remove(c)
	if p.ctl == nil || p.ctl == first {
		t.Fatal("no fresh controller for a pool that came back")
	}
}

// The controller reads its sessions and sets the window, pace and margin on
// all of them: the whole of the wiring, end to end, on a path with a round trip
// and no queue.
func TestPathControlDrivesItsSessions(t *testing.T) {
	s := pckSettings()
	EnablePathControl(&s)
	p := s.Path
	a, b := &fakePath{srtt: 75}, &fakePath{srtt: 75}
	p.add(a, nil)
	p.add(b, nil)
	defer p.remove(a)
	defer p.remove(b)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		rate, margin := a.rate, a.margin
		a.mu.Unlock()
		if rate > 0 && margin > 0 && b.window() == a.window() {
			if b.rate == 0 {
				t.Fatal("one session was paced and the other not")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("not driven after 5 s: rate %d margin %v windows %d/%d", a.rate, a.margin, a.window(), b.window())
}

// The sockets' counters are read once per socket however many sessions share
// it, and the total never falls when a socket goes: a fall would read as a
// burst of refusals to the controller.
func TestPathControlCountsASocketOnceAndNeverGoesBack(t *testing.T) {
	s := pckSettings()
	EnablePathControl(&s)
	p := s.Path
	shared, own := &fakeCarrier{}, &fakeCarrier{}
	shared.refused.Store(5)
	shared.sent.Store(1000)
	own.refused.Store(2)
	own.sent.Store(500)

	a, b, c := &fakePath{}, &fakePath{}, &fakePath{}
	p.add(a, shared)
	p.add(b, shared)
	p.add(c, own)
	_, refused, sent := p.snapshot()
	if refused != 7 || sent != 1500 {
		t.Fatalf("totals %d/%d, want 7/1500: the shared socket counted once", refused, sent)
	}

	own.refused.Store(4)
	own.sent.Store(900)
	p.remove(c)
	_, refused, sent = p.snapshot()
	if refused != 9 || sent != 1900 {
		t.Fatalf("after its socket went: %d/%d, want 9/1900 — what it counted stays", refused, sent)
	}

	p.remove(a)
	_, refused, _ = p.snapshot()
	if refused != 9 {
		t.Fatalf("one of two sessions on the shared socket leaving changed the total to %d", refused)
	}
	p.remove(b)
	if p.ctl != nil {
		t.Fatal("still running with nothing left")
	}
}

// A pooled session is under control only on a tunnel that has a path control,
// and the idle governor's own behaviour is not otherwise changed.
func TestPooledSessionWithoutPathControlIsAnOrdinaryIdleSession(t *testing.T) {
	ft := &fakeTuner{}
	c := &idleKCP{tuner: ft, interval: 10, ack: true}
	if c.path != nil {
		t.Fatal("an idle session has a path control by default")
	}
	// The settle step of a sweep is for sessions that have one; a sweep of one
	// that has none must not need a session behind it.
	c.touch()
	c.sweep(time.Now().Add(idleAfter+time.Second), idleAfter)
	c.touch()
	if iv, ack := ft.get(); iv != 10 || !ack {
		t.Fatalf("interval %d, ack-nodelay %v; want 10 and true", iv, ack)
	}
}

type fakeDelayer struct {
	mu sync.Mutex
	on bool
}

func (f *fakeDelayer) SetWriteDelay(v bool) { f.mu.Lock(); f.on = v; f.mu.Unlock() }
func (f *fakeDelayer) get() bool            { f.mu.Lock(); defer f.mu.Unlock(); return f.on }

// While the path control batches, the session's acknowledgement setting is the
// control's, not the preset's, and an idle session still acknowledges at once
// and takes the control's setting when it wakes.
func TestPathControlOwnsTheAckSettingOfABusySessionOnly(t *testing.T) {
	ft := &fakeTuner{}
	c := &idleKCP{tuner: ft, interval: 10, ack: true, path: &PathControl{}}
	c.touch()

	c.SetACKNoDelay(false) // the control: batch
	if _, ack := ft.get(); ack {
		t.Fatal("a busy session did not take the control's batching")
	}

	c.sweep(time.Now().Add(idleAfter+time.Second), idleAfter)
	if _, ack := ft.get(); !ack {
		t.Fatal("an idle session batches its acknowledgements: the first packet after the pause would wait for one")
	}
	c.SetACKNoDelay(false) // the control changes its mind while the session is idle
	if _, ack := ft.get(); !ack {
		t.Fatal("the control's setting reached a session that is idle")
	}
	c.touch()
	if iv, ack := ft.get(); iv != 10 || ack {
		t.Fatalf("after waking: interval %d ack-nodelay %v, want 10 and the control's (batching)", iv, ack)
	}
}

// A write delay holds a write until the next tick, which on an idle session is
// 200 ms away: the first write after a pause must go out at once, so the delay
// is not in force while idle, nor at the wake, only from the sweep after it.
func TestWriteDelayIsNeverInForceOnTheFirstWriteAfterAPause(t *testing.T) {
	ft, fd := &fakeTuner{}, &fakeDelayer{}
	c := &idleKCP{tuner: ft, delayer: fd, interval: 10, path: &PathControl{}}
	c.touch()

	c.SetWriteDelay(true)
	if fd.get() {
		t.Fatal("the delay took effect at once on a session that had not been swept since it came up")
	}
	c.sweep(time.Now(), idleAfter)
	if !fd.get() {
		t.Fatal("the delay never took effect on a busy session")
	}
	c.SetWriteDelay(true) // already in force: stays
	if !fd.get() {
		t.Fatal("repeating the setting turned it off")
	}

	c.sweep(time.Now().Add(idleAfter+time.Second), idleAfter)
	if fd.get() {
		t.Fatal("an idle session delays its writes")
	}
	c.touch()
	if fd.get() {
		t.Fatal("the delay is in force at the wake: the first write would wait for the idle tick")
	}
	c.sweep(time.Now(), idleAfter)
	if !fd.get() {
		t.Fatal("the delay did not return after the wake")
	}
	c.SetWriteDelay(false)
	if fd.get() {
		t.Fatal("turning the delay off was not immediate")
	}
}

var _ pathctl.Session = (*idleKCP)(nil)
