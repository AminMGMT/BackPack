package direct

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A UDP flow counts against the connection cap.
//
// It has to, and it did not. A flow is recognised by its source address, and a
// UDP source address costs nothing to invent — so an uncapped flow table is one
// smux stream and one socket at the far end for every address a sender cares to
// make up. MaxConnections is documented as the ceiling on forwarded
// connections; leaving the one protocol where the ceiling matters most outside
// it made the setting quietly narrower than it reads.
func TestUDPFlowsCountAgainstTheConnectionCap(t *testing.T) {
	backend := echoUDPBackend(t, "U:")

	const token = "a-udp-cap-token"
	origin, err := NewOrigin(Config{
		Role: RoleOrigin, Addr: "127.0.0.1:0", Token: token,
	}, quietLogger())
	if err != nil {
		t.Fatalf("NewOrigin: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	originDone := make(chan struct{})
	go func() { defer close(originDone); _ = origin.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-originDone })

	port := freePort(t)
	edge, err := NewEdge(Config{
		Role: RoleEdge, Addr: awaitBind(t, origin).String(), Token: token,
		Ports:          []string{fmt.Sprintf("127.0.0.1:%d=%s", port, backend)},
		AcceptUDP:      true,
		MaxConnections: 2,
		RetryDelay:     200 * time.Millisecond,
	}, quietLogger())
	if err != nil {
		t.Fatalf("NewEdge: %v", err)
	}
	edgeDone := make(chan struct{})
	go func() { defer close(edgeDone); _ = edge.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-edgeDone })

	tn := &tunnel{edge: edge, origin: origin, port: port}
	tn.awaitSession(t, 5*time.Second)

	// Two clients, each its own source address and so its own flow, is the
	// whole allowance.
	for i := 0; i < 2; i++ {
		conn := tn.dialUDP(t)
		if _, ok := udpExchange(t, conn, fmt.Sprintf("client-%d", i), 40); !ok {
			t.Fatalf("client %d never got a reply, so the cap was never reached", i)
		}
	}

	if active := edge.Stats().Active; active != 2 {
		t.Fatalf("active flows = %d, want 2", active)
	}

	// A third must be refused. UDP has no way to say so, so what is observable
	// is that no reply comes and the refusal is counted.
	before := edge.Stats().Refused
	third := tn.dialUDP(t)
	if reply, ok := udpExchange(t, third, "third", 6); ok {
		t.Fatalf("a third flow was served past the cap, replying %q", reply)
	}
	if after := edge.Stats().Refused; after <= before {
		t.Fatalf("the refusal was not counted: %d then %d", before, after)
	}

	// And the slot must come back, or a tunnel that has ever been busy stays
	// busy forever. Closing the client is not enough — UDP has no close — so
	// this is the reaper's job, which is why the flow table is checked rather
	// than waited on.
	if active := edge.Stats().Active; active != 2 {
		t.Fatalf("a refused flow took a slot: active = %d, want 2", active)
	}
}
