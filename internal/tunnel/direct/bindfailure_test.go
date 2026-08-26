package direct

import (
	"context"
	"fmt"
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
// It used to probe TCP only, and the UDP tests bind both.
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
