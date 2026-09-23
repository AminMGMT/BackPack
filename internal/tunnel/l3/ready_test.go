package l3

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/testport"
)

// The forwarder has to be able to say when its listeners are up.
//
// UDP gives a client no signal that the far side has bound, so a test that
// sends before the listener exists can only retry. The retry that was here
// looked like a twelve-second budget — sixty sends, each with a 200ms read
// deadline — and was nothing of the kind: a datagram sent to a port nothing has
// bound draws an ICMP port-unreachable back, and a connected UDP socket then
// fails its next Read *immediately* rather than waiting out the deadline.
// Measured on this machine: sixty attempts in 725µs.
//
// So on CI, where the listener goroutine had simply not been scheduled yet, the
// test gave it under a millisecond and then reported
//
//	no reply came back through the udp forwarder
//
// which reads as a forwarder that does not forward. Run took 20s, the test
// itself 0.00s — the tell, if anyone had looked at it.
//
// A guess is the wrong instrument when the answer is knowable. Run knows
// exactly when every socket is bound, so it says so.
func TestTheForwarderSaysWhenItsListenersAreUp(t *testing.T) {
	backend := echoUDP(t, "U:")
	port := testport.Free(t)

	log, _ := testLoggerCapturing(t)
	forwarder, err := NewForwarder(Config{
		Ports:     []string{fmt.Sprintf("127.0.0.1:%d=%s", port, backend)},
		AcceptUDP: true,
		PeerIP:    "127.0.0.1",
	}, log)
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = forwarder.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	select {
	case <-forwarder.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("the forwarder never reported its listeners as up")
	}

	// Ready has to mean bound, not merely attempted: both sockets must refuse
	// a second binding by the time it fires.
	if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
		l.Close()
		t.Errorf("tcp %d was still free when Ready fired", port)
	}
	if pc, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
		pc.Close()
		t.Errorf("udp %d was still free when Ready fired", port)
	}
}

// And a listener that cannot bind must not leave a waiter hanging. Readiness
// means "no longer coming up", not "came up well" — the error is Run's to
// report, and it does, but somebody waiting to send has to be let go either
// way or a bind failure turns into a hang.
func TestReadyFiresEvenWhenAListenerCannotBind(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer taken.Close()

	log, _ := testLoggerCapturing(t)
	forwarder, err := NewForwarder(Config{
		Ports:  []string{fmt.Sprintf("%s=127.0.0.1:9", taken.Addr())},
		PeerIP: "127.0.0.1",
	}, log)
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = forwarder.Run(ctx) }()

	select {
	case <-forwarder.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("Ready never fired for a listener that could not bind")
	}
}
