package direct

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// A forwarded port that cannot be bound has to say so.
//
// This is the mechanism behind a CI failure that read "the udp tunnel never
// came up" and was diagnosed as the tunnel being slow. It is not slow: the
// warmup normally gets its reply on the first attempt, in about twenty
// milliseconds. Forty attempts at half a second each is twenty seconds of no
// reply at all, which is not a tunnel taking its time — it is a listener that
// was never there.
//
// The way it is never there is a port collision. The test harness asks the
// kernel for a free port, closes the socket, and hands the number to the edge
// to bind; anything else on the host may take it in between, and `go test
// ./...` runs packages in parallel while CI runs the whole suite twice, once
// under -race. Raising the retry count cannot help with any of this — the port
// is gone, and waiting longer does not bring it back.
//
// What was missing was the engine's own account of it, which the tests threw
// away. With that kept, a collision is one line naming the port instead of a
// silent wait.
func TestAPortThatCannotBeBoundIsReported(t *testing.T) {
	// Hold the port for the whole test, so the edge cannot have it.
	held, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer held.Close()
	port := held.LocalAddr().(*net.UDPAddr).Port

	log, captured := testLoggerCapturing(t)
	backend := echoUDPBackend(t, "U:")

	// The origin is never reachable here; the forwarders bind before any
	// session exists, which is the behaviour being checked.
	edge, err := NewEdge(Config{
		Role: RoleEdge, Addr: "127.0.0.1:1", Token: "a-udp-token",
		Ports:      []string{fmt.Sprintf("127.0.0.1:%d=%s", port, backend)},
		AcceptUDP:  true,
		RetryDelay: 200 * time.Millisecond,
	}, log)
	if err != nil {
		t.Fatalf("NewEdge: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = edge.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	var out string
	for time.Now().Before(deadline) {
		out = captured.String()
		if strings.Contains(out, fmt.Sprint(port)) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	if !strings.Contains(out, fmt.Sprint(port)) {
		t.Fatalf("a port that was already taken produced no mention of it in the "+
			"tunnel's log, so a collision looks exactly like a tunnel that is "+
			"slow to come up. Logged:\n%s", out)
	}
}

// The harness must not hand out a port that is free for TCP and taken for UDP.
// It used to probe TCP only, and these tests bind both.
func TestFreePortIsFreeForUDPToo(t *testing.T) {
	for i := 0; i < 50; i++ {
		port := freePort(t)

		packet, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("freePort returned %d, which cannot be bound for UDP: %v", port, err)
		}
		packet.Close()
	}
}

// A port must never be issued twice in a run, and must never come from the
// range the kernel draws outgoing source ports from — that overlap is what let
// one of this suite's own connections take a listen port before the tunnel
// bound it, which is the failure CI reported and a developer's machine did not.
func TestPortsAreUniqueAndBelowTheEphemeralRange(t *testing.T) {
	// Linux's ephemeral range starts at 32768, macOS's at 49152. Staying under
	// the lower of the two clears both.
	const lowestEphemeral = 32768

	seen := make(map[int]bool)
	for i := 0; i < 200; i++ {
		port := freePort(t)
		if seen[port] {
			t.Fatalf("port %d was issued twice", port)
		}
		seen[port] = true
		if port >= lowestEphemeral {
			t.Fatalf("port %d is inside the kernel's ephemeral range, where an "+
				"outgoing connection of this suite's own can take it before the "+
				"tunnel binds", port)
		}
	}
}

func TestForwardedPortRecoversAfterTemporaryBindFailure(t *testing.T) {
	for _, protocol := range []string{"tcp", "udp"} {
		t.Run(protocol, func(t *testing.T) {
			blockedAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
			var held io.Closer
			var err error
			if protocol == "tcp" {
				held, err = net.Listen("tcp", blockedAddr)
			} else {
				held, err = net.ListenPacket("udp", blockedAddr)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()

			const token = "forwarder-retry-token"
			origin, err := NewOrigin(Config{Addr: "127.0.0.1:0", Token: token}, quietLogger())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			originDone := make(chan error, 1)
			go func() { originDone <- origin.Run(ctx) }()
			defer func() { cancel(); <-originDone }()
			for deadline := time.Now().Add(3 * time.Second); origin.LocalAddr() == nil; {
				if time.Now().After(deadline) {
					t.Fatal("origin never started")
				}
				time.Sleep(5 * time.Millisecond)
			}

			healthyAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
			healthyBackend := echoBackend(t, "H:")
			blockedBackend := echoBackend(t, "R:")
			if protocol == "udp" {
				blockedBackend = echoUDPBackend(t, "R:")
			}
			log, captured := testLoggerCapturing(t)
			edge, err := NewEdge(Config{
				Addr: origin.LocalAddr().String(), Token: token,
				Ports:     []string{healthyAddr + "=" + healthyBackend, blockedAddr + "=" + blockedBackend},
				AcceptUDP: protocol == "udp", RetryDelay: 30 * time.Millisecond,
			}, log)
			if err != nil {
				t.Fatal(err)
			}
			edgeDone := make(chan error, 1)
			go func() { edgeDone <- edge.Run(ctx) }()
			defer func() { cancel(); <-edgeDone }()
			for deadline := time.Now().Add(3 * time.Second); ; {
				if edge.Stats().Sessions > 0 && strings.Contains(captured.String(), "stopped:") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("session or initial bind failure was not observed")
				}
				time.Sleep(5 * time.Millisecond)
			}

			healthy, err := net.DialTimeout("tcp", healthyAddr, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer healthy.Close()
			exchange := func(conn net.Conn, prefix string) {
				t.Helper()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				if _, err := conn.Write([]byte("hello")); err != nil {
					t.Fatal(err)
				}
				want := prefix + "hello"
				got := make([]byte, len(want))
				if _, err := io.ReadFull(conn, got); err != nil || string(got) != want {
					t.Fatalf("exchange: got %q, error %v, want %q", got, err, want)
				}
			}
			exchange(healthy, "H:")
			if err := held.Close(); err != nil {
				t.Fatal(err)
			}

			var recovered net.Conn
			for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
				if protocol == "tcp" {
					recovered, err = net.DialTimeout("tcp", blockedAddr, 50*time.Millisecond)
					if err == nil {
						break
					}
				} else {
					// A failed bind means UDP is still free; successfully rebinding
					// it ourselves proves the edge has not recovered yet.
					probe, bindErr := net.ListenPacket("udp", blockedAddr)
					if bindErr != nil {
						recovered, err = net.Dial("udp", blockedAddr)
						break
					}
					probe.Close()
				}
				time.Sleep(5 * time.Millisecond)
			}
			if recovered == nil || err != nil {
				t.Fatalf("forwarded %s port stayed down after its temporary bind conflict ended", protocol)
			}
			defer recovered.Close()
			exchange(recovered, "R:")
			exchange(healthy, "H:") // The same established connection must survive.
		})
	}
}

func TestForwarderRetryStopsOnCancellation(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	log, captured := testLoggerCapturing(t)
	edge, err := NewEdge(Config{
		Addr: "127.0.0.1:1", Token: "retry-cancellation",
		Ports:      []string{held.Addr().String() + "=127.0.0.1:1"},
		RetryDelay: time.Hour, DialTimeout: 100 * time.Millisecond,
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- edge.Run(ctx) }()
	for deadline := time.Now().Add(time.Second); !strings.Contains(captured.String(), "stopped:"); {
		if time.Now().After(deadline) {
			t.Fatal("initial bind failure was not observed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("forwarder retry did not stop when the tunnel was cancelled")
	}
}
