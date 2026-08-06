package transport

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestForwardTCPPoolBoundsConcurrentDialsAndRefills(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const workers = 3
	gate := make(chan struct{}, workers+1)
	var calls atomic.Int32
	var peersMu sync.Mutex
	var peers []net.Conn
	dial := func(ctx context.Context) (net.Conn, error) {
		calls.Add(1)
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		client, peer := net.Pipe()
		peersMu.Lock()
		peers = append(peers, peer)
		peersMu.Unlock()
		return client, nil
	}

	pool := newForwardTCPPool(ctx, workers, dial)
	pool.Start()
	t.Cleanup(func() {
		pool.Close()
		peersMu.Lock()
		defer peersMu.Unlock()
		for _, peer := range peers {
			peer.Close()
		}
	})

	waitAtomic(t, &calls, workers)
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != workers {
		t.Fatalf("pool launched %d concurrent dials, want exactly %d", got, workers)
	}

	for range workers {
		gate <- struct{}{}
	}
	conn, err := pool.Get(ctx, time.Second)
	if err != nil {
		t.Fatalf("get pre-warmed connection: %v", err)
	}
	conn.Close()

	// Consuming one slot makes exactly that worker refill it.
	waitAtomic(t, &calls, workers+1)
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != workers+1 {
		t.Fatalf("one checkout triggered %d total dials, want %d", got, workers+1)
	}
}

func TestForwardTCPPoolCancellationUnblocksWaiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pool := newForwardTCPPool(ctx, 1, func(ctx context.Context) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	pool.Start()

	done := make(chan error, 1)
	go func() {
		_, err := pool.Get(ctx, time.Minute)
		done <- err
	}()
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Get returned nil after generation cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("Get remained blocked after generation cancellation")
	}
}

func waitAtomic(t *testing.T, value *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if value.Load() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("counter reached %d, want at least %d", value.Load(), want)
}
