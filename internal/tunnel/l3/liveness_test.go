package l3

import (
	"context"
	"testing"
	"time"
)

// A listener that restarts comes back with no memory of the session. The
// dialler has to notice that nothing is coming back and handshake again,
// rather than sealing into the void until the routine rekey — which was two
// minutes of a dead tunnel after a one-second restart.
func TestADiallerRecoversFromAListenerThatRestarted(t *testing.T) {
	oldSilent, oldCheck := peerSilentAfter, rekeyCheck
	peerSilentAfter, rekeyCheck = 300*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { peerSilentAfter, rekeyCheck = oldSilent, oldCheck })

	const token = "a-liveness-token"
	cfg := func(mode, addr, local, peer string) Config {
		return Config{Mode: mode, Addr: addr, Token: token, Encap: "gre",
			LocalIP: local, PeerIP: peer, MTU: 1400}
	}

	// The first listener, on a context of its own so it can be killed alone.
	firstCtx, killFirst := context.WithCancel(context.Background())
	firstDev := newFakeDevice(1400)
	first, err := New(cfg(ModeListen, "127.0.0.1:0", "10.10.0.2/30", "10.10.0.1"), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	first.openDevice = func(deviceSpec) (packetDevice, error) { return firstDev, nil }
	firstDone := make(chan struct{})
	go func() { defer close(firstDone); _ = first.Run(firstCtx) }()
	addr := awaitBind(t, first).String()

	ctx, cancel := context.WithCancel(context.Background())
	dialDev := newFakeDevice(1400)
	dialer, err := New(cfg(ModeDial, addr, "10.10.0.1/30", "10.10.0.2"), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	dialer.openDevice = func(deviceSpec) (packetDevice, error) { return dialDev, nil }
	start(t, ctx, cancel, dialer, dialDev)
	awaitSession(t, dialer, 5*time.Second)
	across(t, dialDev, firstDev, ipv4Packet(1))

	// The listener goes, and a new one takes its port with no session.
	killFirst()
	firstDev.Close()
	<-firstDone

	secondDev := newFakeDevice(1400)
	second, err := New(cfg(ModeListen, addr, "10.10.0.2/30", "10.10.0.1"), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	second.openDevice = func(deviceSpec) (packetDevice, error) { return secondDev, nil }
	start(t, ctx, cancel, second, secondDev)
	awaitBind(t, second)

	// Keep sending, as a live tunnel would. Something has to come out of the
	// new listener well inside the two-minute rekey.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		dialDev.inject <- ipv4Packet(2)
		select {
		case <-secondDev.emitted:
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("the dialler never re-established with the restarted listener")
}

func TestSilenceIsMeasuredFromTheFirstUnansweredSend(t *testing.T) {
	tun := &Tunnel{cfg: Config{Mode: ModeDial}}
	now := time.Now()
	if tun.peerSilent(now) {
		t.Fatal("silent with nothing sent")
	}
	tun.noteSent()
	first := tun.unanswered.Load()
	tun.noteSent()
	if tun.unanswered.Load() != first {
		t.Fatal("a later send moved the start of the silence")
	}
	if tun.peerSilent(now) {
		t.Fatal("silent the moment something was sent")
	}
	if !tun.peerSilent(now.Add(peerSilentAfter + time.Second)) {
		t.Fatal("not silent after the whole window with no answer")
	}
	tun.noteSent()
	tun.noteAnswered()
	if tun.peerSilent(now.Add(time.Hour)) {
		t.Fatal("silent after an answer")
	}

	// The listening side never decides this: it cannot handshake.
	listen := &Tunnel{cfg: Config{Mode: ModeListen}}
	listen.noteSent()
	if listen.unanswered.Load() != 0 {
		t.Fatal("the listener started a silence clock")
	}
}
