package l3

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// A listener that cannot bind has to say so.
//
// This is what a CI failure in this package actually was, twice diagnosed as a
// flaky UDP test. The port picked for the test was taken between being checked
// and being bound, the UDP listener never came up, and the forwarder said
// nothing at all — Run captured the error and then threw it away, because it
// returned nil whenever the context had since been cancelled, which is every
// shutdown. TCP bound fine, so the forwarder went on working in a way that
// looked entirely healthy while half of what it was asked to carry did not
// exist.
//
// From the outside that is a port that quietly does nothing, which no amount of
// retrying at the other end can fix.
func TestAUDPListenerThatCannotBindIsReported(t *testing.T) {
	port := freePort(t)
	held, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("could not hold the port: %v", err)
	}
	defer held.Close()

	backend := echoUDP(t, "U:")
	log, captured := testLoggerCapturing(t)
	forwarder, err := NewForwarder(Config{
		Ports:     []string{fmt.Sprintf("127.0.0.1:%d=%s", port, backend)},
		AcceptUDP: true, PeerIP: "127.0.0.1",
	}, log)
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- forwarder.Run(ctx) }()

	// Long enough for the bind to be attempted and fail; the TCP half stays up.
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case err := <-runErr:
		if err == nil {
			t.Error("Run reported success although a listener never bound — an " +
				"error raised before cancellation must survive the cancellation")
		} else if !strings.Contains(err.Error(), "address already in use") {
			t.Errorf("Run reported %v, which does not name the bind failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run never returned")
	}

	if out := captured.String(); !strings.Contains(out, fmt.Sprint(port)) {
		t.Errorf("nothing in the log names the port that could not be bound:\n%s", out)
	}
}

// The healthy case must stay quiet: a shutdown is not a failure.
func TestAnOrdinaryShutdownIsNotAnError(t *testing.T) {
	port := freePort(t)
	backend := echoUDP(t, "U:")
	log, _ := testLoggerCapturing(t)
	forwarder, err := NewForwarder(Config{
		Ports:     []string{fmt.Sprintf("127.0.0.1:%d=%s", port, backend)},
		AcceptUDP: true, PeerIP: "127.0.0.1",
	}, log)
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- forwarder.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("a clean shutdown reported %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run never returned")
	}
}

// One forwarded port that something else holds must not take the others down
// with it, and must come up by itself once it is free.
//
// It used to end the forwarder's whole generation, and the supervisor rebuilt
// every listener five seconds later — every port of the tunnel, TCP and UDP,
// dropped and reopened every five seconds for as long as one was held. Seen on
// a real Iran server as UDP at 79% and connection latency past 600 ms.
func TestAHeldPortDoesNotTakeTheOthersDown(t *testing.T) {
	old := forwardBindRetry
	forwardBindRetry = 200 * time.Millisecond
	t.Cleanup(func() { forwardBindRetry = old })

	heldPort, freeP := freePort(t), freePort(t)
	held, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", heldPort))
	if err != nil {
		t.Fatalf("could not hold the port: %v", err)
	}
	backend := echoTCP(t, "E:")
	log, captured := testLoggerCapturing(t)
	forwarder, err := NewForwarder(Config{
		Ports: []string{
			fmt.Sprintf("127.0.0.1:%d=%s", heldPort, backend),
			fmt.Sprintf("127.0.0.1:%d=%s", freeP, backend),
		},
		PeerIP: "127.0.0.1",
	}, log)
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- forwarder.Run(ctx) }()
	<-forwarder.Ready()

	// The free port carries a connection that outlives several bind retries of
	// the held one.
	conn := dialUntilReady(t, fmt.Sprintf("127.0.0.1:%d", freeP))
	defer conn.Close()
	time.Sleep(5 * forwardBindRetry)
	roundTrip(t, conn, "still here")

	// Free the held port: the forwarder takes it without a restart.
	held.Close()
	c2 := dialUntilReady(t, fmt.Sprintf("127.0.0.1:%d", heldPort))
	roundTrip(t, c2, "came up")
	c2.Close()
	roundTrip(t, conn, "never dropped")

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("a port that came up in the end was still reported at shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run never returned")
	}
	if !strings.Contains(captured.String(), fmt.Sprint(heldPort)) {
		t.Errorf("nothing in the log names the port that could not be bound:\n%s", captured.String())
	}
}

// roundTrip writes msg on conn and wants the echo backend's answer.
func roundTrip(t *testing.T, conn net.Conn, msg string) {
	t.Helper()
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatalf("write %q: %v", msg, err)
	}
	want := "E:" + msg
	buf := make([]byte, len(want))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read after %q: %v", msg, err)
	}
	if string(buf) != want {
		t.Fatalf("read %q, want %q", buf, want)
	}
}
