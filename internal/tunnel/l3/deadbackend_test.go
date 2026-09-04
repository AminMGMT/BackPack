package l3

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// A forwarded port whose far side has nothing listening has to say so.
//
// This is the bug report "pck direct does not work", and the reason it was so
// hard to answer: every signal an operator can see says the tunnel is healthy.
// Both ends log a session, the MTU probe crosses and comes back, ping is clean,
// rekeys land on schedule. All of that is true — the tunnel is fine. What is
// missing is the service behind the mapped port at the far end, and every
// connection through the port dies on the dial.
//
// That failure was logged at debug, so the operator was left holding a green
// tunnel and a dead port with nothing in between to connect them. Anyone
// reading the log would go on suspecting the carrier, which is the one part
// that was working.
func TestADeadBackendIsReported(t *testing.T) {
	// A port nothing is listening on: the far-side service that is not there.
	dead := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	port := freePort(t)

	log, captured := testLoggerCapturing(t)
	forwarder, err := NewForwarder(Config{
		Ports:  []string{fmt.Sprintf("127.0.0.1:%d=%s", port, dead)},
		PeerIP: "127.0.0.1",
	}, log)
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = forwarder.Run(ctx) }()

	conn := dialUntilReady(t, fmt.Sprintf("127.0.0.1:%d", port))
	conn.Close()

	waitFor(t, captured, "nothing is answering",
		"the forwarder refused a connection because its backend is gone and never said so")

	said := captured.String()
	if !strings.Contains(said, dead) {
		t.Errorf("the warning does not name the backend that is not answering:\n%s", said)
	}
	if !strings.Contains(said, "The tunnel itself is up") {
		t.Errorf("the warning does not separate the tunnel from the service behind it, "+
			"which is the whole reason this failure is misread:\n%s", said)
	}
}

// And it has to say when the service comes back, because the recovery is the
// half that tells the operator their fix worked.
func TestABackendComingBackIsReported(t *testing.T) {
	// Hold the port, so the address is ours to hand back later.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not reserve a port: %v", err)
	}
	target := held.Addr().String()
	held.Close()

	port := freePort(t)
	log, captured := testLoggerCapturing(t)
	forwarder, err := NewForwarder(Config{
		Ports:  []string{fmt.Sprintf("127.0.0.1:%d=%s", port, target)},
		PeerIP: "127.0.0.1",
	}, log)
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = forwarder.Run(ctx) }()

	front := fmt.Sprintf("127.0.0.1:%d", port)
	conn := dialUntilReady(t, front)
	conn.Close()
	waitFor(t, captured, "nothing is answering", "the dead backend was never reported")

	// The operator starts the service.
	back, err := net.Listen("tcp", target)
	if err != nil {
		t.Skipf("the reserved port was taken before the service could start: %v", err)
	}
	defer back.Close()
	go func() {
		for {
			c, err := back.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	// Retried: the listener above and the forwarder's dial race by a hair.
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		c, err := net.DialTimeout("tcp", front, time.Second)
		if err == nil {
			c.Close()
		}
		if strings.Contains(captured.String(), "is answering again") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("the backend came back and the log never said so:\n%s", captured.String())
}

// A port under load must not write a line per connection: the report is about
// the transition, not the traffic.
func TestADeadBackendIsNotRepeatedPerConnection(t *testing.T) {
	dead := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	port := freePort(t)

	log, captured := testLoggerCapturing(t)
	forwarder, err := NewForwarder(Config{
		Ports:  []string{fmt.Sprintf("127.0.0.1:%d=%s", port, dead)},
		PeerIP: "127.0.0.1",
	}, log)
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = forwarder.Run(ctx) }()

	front := fmt.Sprintf("127.0.0.1:%d", port)
	dialUntilReady(t, front).Close()
	waitFor(t, captured, "nothing is answering", "the dead backend was never reported")

	for i := 0; i < 20; i++ {
		if c, err := net.DialTimeout("tcp", front, time.Second); err == nil {
			c.Close()
		}
	}
	time.Sleep(200 * time.Millisecond)

	if n := strings.Count(captured.String(), "nothing is answering"); n != 1 {
		t.Errorf("21 refused connections produced %d warnings, want 1 — a busy port "+
			"would drown its own log:\n%s", n, captured.String())
	}
}

// waitFor blocks until the captured log contains want.
func waitFor(t *testing.T, captured *capturedLog, want, complaint string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if strings.Contains(captured.String(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s\nlog was:\n%s", complaint, captured.String())
}
