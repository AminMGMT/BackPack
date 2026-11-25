package transport

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/utils"
)

// The quiet reader initially has a short remaining budget. A real packet in
// the opposite direction must refresh that budget, so a later reply survives.
func TestRawUDPIdleBudgetTracksBothDirections(t *testing.T) {
	listen := func() *net.UDPConn {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	server, backend := listen(), listen()
	dial := func(peer *net.UDPConn) *net.UDPConn {
		c, err := net.DialUDP("udp", nil, peer.LocalAddr().(*net.UDPAddr))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	tunnel, local := dial(server), dial(backend)
	c := &UdpTransport{config: &UdpConfig{}, lifecycle: lifecycle{logger: silentLogger()}}
	started := time.Now().Add(-60*time.Second + time.Second)
	var activity atomic.Int64
	done := make(chan struct{}, 2)
	go func() { c.udpCopy(tunnel, local, 0, false, started, &activity); done <- struct{}{} }()
	go func() { c.udpCopy(local, tunnel, 0, true, started, &activity); done <- struct{}{} }()
	t.Cleanup(func() {
		tunnel.Close()
		local.Close()
		for i := 0; i < 2; i++ {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("copy worker leaked")
			}
		}
	})
	time.Sleep(50 * time.Millisecond)
	if _, err := server.WriteToUDP([]byte("upload"), tunnel.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	backend.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 64)
	n, addr, err := backend.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "upload" {
		t.Fatalf("upload=%q", buf[:n])
	}
	time.Sleep(1100 * time.Millisecond)
	select {
	case <-done:
		done <- struct{}{}
		t.Fatal("one direction expired despite opposite traffic")
	default:
	}
	if _, err := backend.WriteToUDP([]byte("reply"), addr); err != nil {
		t.Fatal(err)
	}
	server.SetReadDeadline(time.Now().Add(time.Second))
	n, _, err = server.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "reply" {
		t.Fatalf("reply=%q", buf[:n])
	}
}

func TestRawUDPExpiredFlowReturnsWithoutAnotherPacket(t *testing.T) {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	c := &UdpTransport{config: &UdpConfig{}, lifecycle: lifecycle{logger: silentLogger()}}
	var activity atomic.Int64
	done := make(chan struct{})
	go func() { defer close(done); c.udpCopy(pc, pc, 0, false, time.Now().Add(-61*time.Second), &activity) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		pc.Close()
		<-done
		t.Fatal("fully idle flow waited for another packet")
	}
}

type loopProbe struct {
	server   controlwire.Link // the far end, as the server holds it
	restarts atomic.Int32
	dials    atomic.Int32
	life     *lifecycle
	finished chan struct{}
}

func loopTCPPair(t *testing.T) (a, b net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); got <- c }()
	a, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b = <-got
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

// startClientLoop runs the client's control loop for a generation on ctx, over
// a loopback connection whose other end the test plays the server on.
func startClientLoop(t *testing.T, ctx context.Context, echoBeats bool) *loopProbe {
	t.Helper()
	cli, srv := loopTCPPair(t)
	return startClientLoopOver(t, ctx, controlwire.Net(cli), controlwire.Net(srv), echoBeats)
}

func startClientLoopOver(t *testing.T, ctx context.Context, cli, srv controlwire.Link, echoBeats bool) *loopProbe {
	t.Helper()
	p := &loopProbe{server: srv, finished: make(chan struct{})}
	p.life = &lifecycle{}
	p.life.firstGeneration(ctx, silentLogger(), usageSpec{})
	loop := p.life.control(cli, time.Minute,
		func() { p.dials.Add(1) }, func() { p.restarts.Add(1) })
	loop.echoBeats = echoBeats
	go func() { loop.run(); close(p.finished) }()
	return p
}

func (p *loopProbe) send(t *testing.T, signals ...byte) {
	t.Helper()
	for _, s := range signals {
		if err := p.server.Send(s); err != nil {
			t.Fatal(err)
		}
	}
}

func (p *loopProbe) next(t *testing.T) byte {
	t.Helper()
	_ = p.server.SetReadDeadline(time.Now().Add(2 * time.Second))
	b, err := p.server.Receive()
	if err != nil {
		t.Fatalf("nothing from the client: %v", err)
	}
	return b
}

func (p *loopProbe) silent(t *testing.T) {
	t.Helper()
	_ = p.server.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if b, err := p.server.Receive(); err == nil {
		t.Fatalf("the client sent %d", b)
	}
}

func eventuallyTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAServerRequestDialsAPoolConnection(t *testing.T) {
	p := startClientLoop(t, context.Background(), false)
	p.send(t, utils.SG_Chan, utils.SG_Chan)
	eventuallyTrue(t, "two dials", func() bool { return p.dials.Load() == 2 })
	if n := atomic.LoadInt32(&p.life.loadConnections); n != 2 {
		t.Fatalf("load = %d, want 2", n)
	}
}

// The pool shrinks by leaving a request unanswered: a retirement it has asked
// for takes the next request instead of a dial.
func TestARetirementTheClientAskedForAbsorbsTheNextRequest(t *testing.T) {
	p := startClientLoop(t, context.Background(), false)
	p.life.controlFlow <- struct{}{}
	p.send(t, utils.SG_Chan, utils.SG_Chan)
	eventuallyTrue(t, "a dial", func() bool { return p.dials.Load() == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := p.dials.Load(); n != 1 {
		t.Fatalf("dials = %d, want 1", n)
	}
}

func TestTheRoundTripProbeIsAnswered(t *testing.T) {
	p := startClientLoop(t, context.Background(), false)
	p.send(t, utils.SG_RTT)
	if got := p.next(t); got != utils.SG_RTT {
		t.Fatalf("answered %d, want the probe back", got)
	}
}

// Only the websocket servers listen for an answered heartbeat. The stream
// servers of earlier releases read one byte from the client and stop, so an
// answer sent to one would be the only thing it ever heard — the goodbye after
// it would go unread.
func TestAHeartbeatIsAnsweredOnlyWhereTheServerListensForIt(t *testing.T) {
	wsServer, wsClient := wsPair(t)
	ws := startClientLoopOver(t, context.Background(), controlwire.WS(wsClient), controlwire.WS(wsServer), true)
	ws.send(t, utils.SG_HB)
	if got := ws.next(t); got != utils.SG_HB {
		t.Fatalf("answered %d, want a heartbeat", got)
	}

	stream := startClientLoop(t, context.Background(), false)
	stream.send(t, utils.SG_HB)
	stream.silent(t)
}

func TestTheServersGoodbyeRestartsOnce(t *testing.T) {
	p := startClientLoop(t, context.Background(), false)
	p.send(t, utils.SG_Closed)
	p.waitFinished(t)
	p.server.Close() // the reader now fails; it must not ask again
	time.Sleep(100 * time.Millisecond)
	if n := p.restarts.Load(); n != 1 {
		t.Fatalf("restarts = %d, want 1", n)
	}
}

func TestAnUnknownSignalRestarts(t *testing.T) {
	p := startClientLoop(t, context.Background(), false)
	p.send(t, 0xEE)
	p.waitFinished(t)
	eventuallyTrue(t, "a restart", func() bool { return p.restarts.Load() == 1 })
}

func TestTheEndOfAGenerationTellsTheServerAndClosesTheChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := startClientLoop(t, ctx, false)
	cancel()
	if got := p.next(t); got != utils.SG_Closed {
		t.Fatalf("sent %d, want the goodbye", got)
	}
	// And the channel goes with the generation: left open, it outlived every
	// generation that ended without a restart — a reload, a fallback chain
	// moving on — and over a datagram carrier nothing else would close it.
	_ = p.server.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := p.server.Receive(); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("the channel stayed open after the goodbye: %v", err)
	}
	p.waitFinished(t)
	if n := p.restarts.Load(); n != 0 {
		t.Fatalf("an ended generation asked for %d restarts", n)
	}
}

func TestALostChannelRestarts(t *testing.T) {
	p := startClientLoop(t, context.Background(), false)
	p.server.Close()
	eventuallyTrue(t, "a restart", func() bool { return p.restarts.Load() == 1 })
}

// waitFinished waits for the loop to return, failing rather than hanging when
// a regression keeps it running.
func (p *loopProbe) waitFinished(t *testing.T) {
	t.Helper()
	select {
	case <-p.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the control loop did not return")
	}
}
