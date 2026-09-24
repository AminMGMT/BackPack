package network

import (
	"io"
	"sync"
	"testing"
	"time"

	"github.com/xtaci/kcp-go/v5"
)

type fakeTuner struct {
	mu       sync.Mutex
	interval int
	ack      bool
}

func (f *fakeTuner) SetNoDelay(_, interval, _, _ int) {
	f.mu.Lock()
	f.interval = interval
	f.mu.Unlock()
}

func (f *fakeTuner) SetACKNoDelay(v bool) {
	f.mu.Lock()
	f.ack = v
	f.mu.Unlock()
}

func (f *fakeTuner) get() (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.interval, f.ack
}

func TestAQuietSessionSlowsDownAndComesBackOnTraffic(t *testing.T) {
	ft := &fakeTuner{interval: 10}
	c := &idleKCP{tuner: ft, interval: 10, ack: false}
	c.touch()

	c.sweep(time.Now(), idleAfter)
	if iv, _ := ft.get(); iv != 10 {
		t.Fatalf("a session that just carried traffic went to %d ms", iv)
	}

	c.sweep(time.Now().Add(idleAfter+time.Second), idleAfter)
	if iv, ack := ft.get(); iv != idleInterval || !ack {
		t.Fatalf("quiet session: interval %d, ack-nodelay %v; want %d and true", iv, ack, idleInterval)
	}

	c.touch()
	if iv, ack := ft.get(); iv != 10 || ack {
		t.Fatalf("after traffic: interval %d, ack-nodelay %v; want the tunnel's own 10 and false", iv, ack)
	}
}

// The control channel forces ack-nodelay on; coming back from idle must not
// turn it off.
func TestComingBackRestoresTheSessionsOwnAckSetting(t *testing.T) {
	ft := &fakeTuner{}
	c := &idleKCP{tuner: ft, interval: 20, ack: true}
	c.touch()
	c.sweep(time.Now().Add(idleAfter+time.Second), idleAfter)
	c.touch()
	if iv, ack := ft.get(); iv != 20 || !ack {
		t.Fatalf("interval %d, ack-nodelay %v; want 20 and true", iv, ack)
	}
}

// Traffic racing the switch to idle must never leave the session idle.
func TestTrafficDuringTheSwitchWins(t *testing.T) {
	for range 2000 {
		ft := &fakeTuner{}
		c := &idleKCP{tuner: ft, interval: 10}
		c.last.Store(time.Now().Add(-time.Hour).UnixNano())
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); c.sweep(time.Now(), idleAfter) }()
		go func() { defer wg.Done(); c.touch() }()
		wg.Wait()
		if iv, _ := ft.get(); c.idle.Load() || iv == idleInterval {
			t.Fatal("a session that just carried traffic was left on the idle interval")
		}
	}
}

// End to end over real KCP sessions: after an idle spell the first exchange
// is not held back by the slow timer.
func TestTheFirstExchangeAfterIdleIsFast(t *testing.T) {
	const after, every = 300 * time.Millisecond, 100 * time.Millisecond
	setGovernor := func(after, every time.Duration) {
		idleGov.mu.Lock()
		idleGov.after, idleGov.every = after, every
		idleGov.mu.Unlock()
	}
	setGovernor(after, every)
	defer setGovernor(idleAfter, idleSweep)

	s := KCPSettings{MTU: 1350, Interval: 10, Resend: 2, NoDelay: 1, NoCongestion: 1, SndWnd: 128, RcvWnd: 128}
	ln, err := kcp.ListenWithOptions("127.0.0.1:0", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan *kcp.UDPSession, 1)
	go func() {
		sess, err := ln.AcceptKCP()
		if err == nil {
			accepted <- sess
		}
	}()
	raw, err := kcp.DialWithOptions(ln.Addr().String(), nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ApplyKCPSettings(raw, s)
	client := IdleAwareKCP(raw, s, false)
	defer client.Close()
	client.Write([]byte{0})
	peer := <-accepted
	ApplyKCPSettings(peer, s)
	server := IdleAwareKCP(peer, s, false)
	defer server.Close()
	go func() { // echo
		buf := make([]byte, 64)
		for {
			n, err := server.Read(buf)
			if err != nil {
				return
			}
			server.Write(buf[:n])
		}
	}()

	roundTrip := func() time.Duration {
		began := time.Now()
		client.Write([]byte("ping"))
		if _, err := io.ReadFull(client, make([]byte, 4)); err != nil {
			t.Fatal(err)
		}
		return time.Since(began)
	}
	io.ReadFull(client, make([]byte, 1))
	roundTrip()

	time.Sleep(after + 3*every)
	if !client.(*idleKCP).idle.Load() || !server.(*idleKCP).idle.Load() {
		t.Fatal("neither side went idle")
	}
	if took := roundTrip(); took > time.Duration(idleInterval/2)*time.Millisecond {
		t.Fatalf("the first round trip after idle took %s; the idle timer held it back", took)
	}
	if client.(*idleKCP).idle.Load() || server.(*idleKCP).idle.Load() {
		t.Fatal("traffic did not bring the sessions back")
	}
}

// Closing through the wrapper lets the governor forget the session.
func TestAClosedSessionLeavesTheGovernor(t *testing.T) {
	raw, err := kcp.DialWithOptions("127.0.0.1:9", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := IdleAwareKCP(raw, KCPSettings{Interval: 10}, false)
	c.Close()
	idleGov.mu.Lock()
	_, still := idleGov.conns[c.(*idleKCP)]
	idleGov.mu.Unlock()
	if still {
		t.Fatal("the governor still holds a closed session")
	}
}
