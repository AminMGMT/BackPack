package transport

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueuedLocalConnectionExpiresAndReleasesQuota(t *testing.T) {
	limits := newLimiter(Limits{MaxConnections: 1})
	if !limits.acquire() {
		t.Fatal("could not reserve the test connection slot")
	}
	server, peer := net.Pipe()
	defer peer.Close()

	var released atomic.Int32
	conn := newLocalTCPConnWithTimeout(server, "127.0.0.1:8080", limits, 20*time.Millisecond, func() {
		released.Add(1)
	})

	select {
	case <-conn.expiry():
	case <-time.After(time.Second):
		t.Fatal("queued connection did not expire")
	}
	if conn.claim() {
		t.Fatal("an expired connection was claimed by a relay")
	}
	if got := limits.active.Load(); got != 0 {
		t.Fatalf("active quota = %d after expiry, want 0", got)
	}
	if got := released.Load(); got != 1 {
		t.Fatalf("release callback ran %d times, want once", got)
	}

	peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("expired queued socket remained open")
	}

	// Cleanup paths may race with the timer; release must remain exactly once.
	conn.closeAndRelease()
	if got := released.Load(); got != 1 {
		t.Fatalf("idempotent close ran release callback %d times", got)
	}
}

func TestClaimedLocalConnectionOutlivesQueueDeadline(t *testing.T) {
	limits := newLimiter(Limits{MaxConnections: 1})
	if !limits.acquire() {
		t.Fatal("could not reserve the test connection slot")
	}
	server, peer := net.Pipe()
	defer peer.Close()

	conn := newLocalTCPConnWithTimeout(server, "127.0.0.1:8080", limits, 20*time.Millisecond, nil)
	if !conn.claim() {
		t.Fatal("fresh connection could not be claimed")
	}
	time.Sleep(50 * time.Millisecond)

	select {
	case <-conn.expiry():
		t.Fatal("queue timer closed a connection after relay handoff")
	default:
	}
	if got := limits.active.Load(); got != 1 {
		t.Fatalf("active quota = %d while relay owns it, want 1", got)
	}

	conn.closeAndRelease()
	if got := limits.active.Load(); got != 0 {
		t.Fatalf("active quota = %d after relay completion, want 0", got)
	}
}

func TestDrainLocalTCPClosesEveryQueuedConnection(t *testing.T) {
	limits := newLimiter(Limits{MaxConnections: 2})
	queue := make(chan LocalTCPConn, 2)
	for i := 0; i < 2; i++ {
		if !limits.acquire() {
			t.Fatal("could not reserve connection slot")
		}
		server, peer := net.Pipe()
		defer peer.Close()
		queue <- newLocalTCPConnWithTimeout(server, "127.0.0.1:8080", limits, time.Minute, nil)
	}

	drainLocalTCP(queue)
	if got := len(queue); got != 0 {
		t.Fatalf("queue still contains %d connections", got)
	}
	if got := limits.active.Load(); got != 0 {
		t.Fatalf("active quota = %d after drain, want 0", got)
	}
}
