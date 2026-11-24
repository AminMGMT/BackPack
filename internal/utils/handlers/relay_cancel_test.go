package handlers

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/web"
)

// Cancellation is the shutdown path a reload and a transport restart both take.
// The connection it has to interrupt is the awkward one: nothing is being sent
// on it in either direction, so neither Read will ever return on its own. If
// the handler cannot get out of that, a reloaded tunnel keeps the previous
// generation's forwarded connections open for the life of the process.
func TestTCPRelayCancellationClosesBothEnds(t *testing.T) {
	client, from := tcpPair(t)
	to, backend := tcpPair(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		TCPConnectionHandler(ctx, false, from, to, quietLogger(), &web.Usage{}, 8080, false)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("an idle relay did not return after its context was cancelled")
	}

	for name, conn := range map[string]net.Conn{"client": client, "backend": backend} {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Read(make([]byte, 1)); err == nil {
			t.Errorf("the %s side stayed open after cancellation", name)
		}
	}
}

// The caller releases its connection quota when the handler returns, so the
// handler must not return while either copy is still running.
func TestRelayReturnsOnlyAfterBothCopiesStop(t *testing.T) {
	client, from := tcpPair(t)
	to, backend := tcpPair(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		TCPConnectionHandler(ctx, false, from, to, quietLogger(), &web.Usage{}, 8080, false)
	}()

	// After one direction ends, cancellation still has to interrupt and join
	// the response copy before the caller can release its connection quota.
	client.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay did not join both copies after cancellation")
	}

	// Both ends are closed by the time it returns, which is what makes the
	// other copy's Read return rather than linger.
	backend.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := backend.Read(make([]byte, 1)); err == nil {
		t.Error("the far end was still open after the relay returned")
	}
}
