package direct

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/utils/acceptloop"
)

// A host that fills the origin's allowance of unproven connections has its
// next one closed at once, instead of each one holding a goroutine for the
// whole handshake timeout.
func TestTheOriginBoundsConnectionsStillInTheirHandshake(t *testing.T) {
	origin, err := NewOrigin(Config{
		Role: RoleOrigin, Addr: "127.0.0.1:0", Token: "a-long-enough-token", Transport: "tcp",
	}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	origin.preauth = acceptloop.Gate{PerHost: 2, Total: 2}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = origin.Run(ctx) }()
	defer func() { cancel(); <-done }()

	var bound net.Addr
	for deadline := time.Now().Add(3 * time.Second); bound == nil && time.Now().Before(deadline); {
		bound = origin.LocalAddr()
		time.Sleep(5 * time.Millisecond)
	}
	if bound == nil {
		t.Fatal("the origin never bound a port")
	}

	// Two strangers that say nothing hold the allowance.
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", bound.String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
	}
	time.Sleep(100 * time.Millisecond)

	third, err := net.Dial("tcp", bound.String())
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	_ = third.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := third.Read(make([]byte, 1)); err == nil || isTimeout(err) {
		t.Fatalf("a connection past the allowance was held rather than closed: %v", err)
	}
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}
