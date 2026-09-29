package transport

import (
	"context"
	"io"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/utils"
)

// startLoop runs a control loop over one end of a loopback TCP connection and returns the other
// end, as the client would hold it, and how many restarts the loop asked for.
func startLoop(t *testing.T, ctx context.Context, tweak func(*controlLoop)) (client net.Conn, restarts *atomic.Int32, finished <-chan struct{}) {
	t.Helper()
	client, server := connPair(t, "127.0.0.1")
	restarts = new(atomic.Int32)
	loop := controlLoop{
		ctx:      ctx,
		link:     controlwire.Net(server),
		beat:     40 * time.Second,
		requests: make(chan struct{}),
		log:      silentLogger(),
		restart:  func() { restarts.Add(1) },
	}
	if tweak != nil {
		tweak(&loop)
	}
	done := make(chan struct{})
	go func() { loop.run(); close(done) }()
	return client, restarts, done
}

func readSignal(t *testing.T, c net.Conn, within time.Duration) byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(within))
	b, err := utils.ReceiveBinaryByte(c)
	if err != nil {
		t.Fatalf("no signal within %s: %v", within, err)
	}
	return b
}

// A crashed server leaves its client waiting only as long as a few missed
// beats, so a long configured heartbeat must not slow the first ones.
func TestTheControlLoopBeatsOnTheLivenessSchedule(t *testing.T) {
	client, _, _ := startLoop(t, context.Background(), nil)
	if got := readSignal(t, client, time.Second); got != utils.SG_HB {
		t.Fatalf("first signal = %d, want a heartbeat", got)
	}
}

func TestAQueuedUserAsksTheClientForAConnection(t *testing.T) {
	requests := make(chan struct{}, 1)
	client, _, _ := startLoop(t, context.Background(), func(l *controlLoop) {
		l.beat = time.Hour
		l.requests = requests
	})
	requests <- struct{}{}
	for {
		switch readSignal(t, client, time.Second) {
		case utils.SG_Chan:
			return
		case utils.SG_HB:
		default:
			t.Fatal("unexpected signal")
		}
	}
}

// The end of a generation is told to the client and closes the channel, so the
// client redials at once instead of waiting out its deadline.
func TestTheEndOfAGenerationSaysGoodbyeAndClosesTheChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bye := newFarewell()
	client, restarts, finished := startLoop(t, ctx, func(l *controlLoop) { l.bye = bye })
	cancel()
	for {
		b := readSignal(t, client, time.Second)
		if b == utils.SG_Closed {
			break
		}
	}
	if _, err := utils.ReceiveBinaryByte(client); err == nil || !isEOF(err) {
		t.Fatalf("after the goodbye the channel should be closed, got %v", err)
	}
	<-finished
	bye.wait(0)
	select {
	case <-bye.done:
	default:
		t.Fatal("the farewell was never marked said")
	}
	if n := restarts.Load(); n != 0 {
		t.Fatalf("an ended generation asked for %d restarts", n)
	}
}

func isEOF(err error) bool {
	for e := err; e != nil; {
		if e == io.EOF || e == io.ErrClosedPipe {
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

func TestTheClientsGoodbyeRestartsTheTransportOnce(t *testing.T) {
	client, restarts, finished := startLoop(t, context.Background(), nil)
	if err := utils.SendBinaryByte(client, utils.SG_Closed); err != nil {
		t.Fatal(err)
	}
	<-finished
	eventually(t, 3*time.Second, func() bool { return restarts.Load() == 1 }, "the goodbye asked for no restart")
	client.Close() // the reader now fails; it must not ask again
	time.Sleep(100 * time.Millisecond)
	if n := restarts.Load(); n != 1 {
		t.Fatalf("restarts = %d, want 1", n)
	}
}

// Every byte after the first is heard: the goodbye of a client that has
// already sent something else still ends the generation.
func TestTheLoopKeepsListeningAfterTheFirstSignal(t *testing.T) {
	client, restarts, finished := startLoop(t, context.Background(), nil)
	for _, b := range []byte{utils.SG_HB, 0xEE, utils.SG_HB, utils.SG_Closed} {
		if err := utils.SendBinaryByte(client, b); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("the goodbye after other signals went unheard")
	}
	eventually(t, 3*time.Second, func() bool { return restarts.Load() == 1 }, "the goodbye asked for no restart")
}

func TestALostChannelAsksForARestart(t *testing.T) {
	client, restarts, _ := startLoop(t, context.Background(), nil)
	client.Close()
	eventually(t, 3*time.Second, func() bool { return restarts.Load() == 1 }, "timed out waiting for a restart")
}

func TestTheRoundTripIsProbedOnlyWhenAsked(t *testing.T) {
	client, _, _ := startLoop(t, context.Background(), func(l *controlLoop) { l.beat = time.Hour; l.probeRTT = true })
	if got := readSignal(t, client, time.Second); got != utils.SG_RTT {
		t.Fatalf("first signal = %d, want the RTT probe", got)
	}

	// The other transports' clients restart on a signal they do not expect,
	// so they must never be sent one.
	plain, _, _ := startLoop(t, context.Background(), func(l *controlLoop) { l.beat = time.Hour })
	if got := readSignal(t, plain, time.Second); got != utils.SG_HB {
		t.Fatalf("a loop without the probe opened with %d, want a heartbeat", got)
	}
}

// The loop writes to the channel its generation started with, never to the one
// the transport holds by the time a late goroutine gets round to it — that is
// the next generation's, and a stray goodbye on it ends a healthy tunnel.
func TestAGenerationsGoodbyeNeverReachesTheNextGenerationsChannel(t *testing.T) {
	_, oldServer := connPair(t, "127.0.0.1")
	newClient, newServer := connPair(t, "127.0.0.1")

	s := &TcpTransport{
		lifecycle: lifecycle{logger: silentLogger()},
		config:    &TcpConfig{Heartbeat: time.Hour},
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.controlChannel.Set(oldServer)
	loop := s.control(&tcpGen{ctx: ctx, reqNewConnChan: make(chan struct{})}, ctx, func() {})
	s.controlChannel.Set(newServer) // the next generation has adopted its channel

	loop.probeRTT = false
	cancel()
	done := make(chan struct{})
	go func() { loop.run(); close(done) }()

	<-done
	_ = newClient.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if b, err := utils.ReceiveBinaryByte(newClient); err == nil {
		t.Fatalf("the ended generation wrote %d on the next generation's channel", b)
	}
}

// Once the loop has returned its reader must not stay blocked handing over a
// signal nobody will take.
func TestTheReaderEndsWithTheLoop(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		client, _, finished := startLoop(t, context.Background(), func(l *controlLoop) { l.beat = time.Hour })
		for _, b := range []byte{utils.SG_Closed, utils.SG_HB, utils.SG_HB} {
			if utils.SendBinaryByte(client, b) != nil {
				break
			}
		}
		<-finished
		client.Close()
	}
	eventually(t, 3*time.Second, func() bool { return runtime.NumGoroutine() <= before+2 }, "timed out waiting for the readers to end")
}
