package transport

import (
	"sync"
	"sync/atomic"
	"testing"
)

// The blind spot this exists to close: a few long-lived streams saturate the
// connections they already have without ever asking for a new one, so the
// request-rate trigger stays silent while the pipe is full.
func TestThroughputTriggerGrowsWhenConnectionsAreLoaded(t *testing.T) {
	var p poolLoad
	// 8 live connections carrying 200 Mbit/s = 25 Mbit/s each: working hard.
	if !p.wantsMore(200, 8, 8, 8) {
		t.Fatal("a loaded pool should be allowed to grow")
	}
}

// The same total throughput spread over enough connections is not a reason to
// add more — each one is barely working.
func TestThroughputTriggerIgnoresLightlyLoadedPool(t *testing.T) {
	var p poolLoad
	// 32 connections sharing 200 Mbit/s ≈ 6 Mbit/s each.
	if p.wantsMore(200, 32, 32, 8) {
		t.Fatal("a lightly loaded pool must not grow")
	}
}

// An idle tunnel must never grow the pool.
func TestThroughputTriggerIgnoresIdleTunnel(t *testing.T) {
	var p poolLoad
	if p.wantsMore(0, 8, 8, 8) {
		t.Fatal("no traffic must not grow the pool")
	}
}

// Growth is bounded, so neither trigger can add connections forever.
func TestPoolGrowthIsBounded(t *testing.T) {
	var p poolLoad
	max := 8 * poolGrowthLimit
	if p.wantsMore(10000, 8, max, 8) {
		t.Fatalf("pool must not grow past %d connections", max)
	}
	if !poolCanGrow(max-1, 8) {
		t.Fatal("pool should still grow just below the limit")
	}
	if poolCanGrow(max, 8) {
		t.Fatal("pool must stop at the limit")
	}
}

// A first reading has nothing to compare against, and counters that go
// backwards (a restored metrics file) are not a throughput measurement — both
// must read as "no opinion" rather than as a huge or negative rate.
func TestFirstReadingAndCounterResetAreNotMeasurements(t *testing.T) {
	var p poolLoad
	if got := p.mbps(); got != 0 {
		t.Fatalf("first reading = %d, want 0", got)
	}
	// Simulate the counters having been higher before (a restore).
	p.lastIn, p.lastOut = 1<<40, 1<<40
	if got := p.mbps(); got != 0 {
		t.Fatalf("counter reset = %d, want 0", got)
	}
}

// A pool with no configured size (an unset config) must not be grown.
func TestUnconfiguredPoolDoesNotGrow(t *testing.T) {
	if poolCanGrow(1, 0) {
		t.Fatal("an unconfigured pool size must not grow")
	}
}

func TestMuxPoolLimitBoundsReceiveWindows(t *testing.T) {
	tests := []struct {
		name          string
		configured    int
		receiveBuffer int
		want          int
	}{
		{name: "default keeps fourfold growth", configured: 8, receiveBuffer: 4 * 1024 * 1024, want: 32},
		{name: "turbo stops at configured pool", configured: 8, receiveBuffer: 16 * 1024 * 1024, want: 8},
		{name: "aggressive stops at configured pool", configured: 16, receiveBuffer: 32 * 1024 * 1024, want: 16},
		{name: "explicit pool is always honoured", configured: 64, receiveBuffer: 4 * 1024 * 1024, want: 64},
		{name: "invalid receive buffer uses normal cap", configured: 8, receiveBuffer: 0, want: 32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := muxPoolLimit(tt.configured, tt.receiveBuffer); got != tt.want {
				t.Fatalf("muxPoolLimit(%d, %d) = %d, want %d", tt.configured, tt.receiveBuffer, got, tt.want)
			}
		})
	}
}

func TestSessionSlotsCannotRacePastLimit(t *testing.T) {
	const limit = 8
	slots := newSessionSlots(limit)
	start := make(chan struct{})
	var acquired atomic.Int32
	var wg sync.WaitGroup

	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if slots.tryAcquire() {
				acquired.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := acquired.Load(); got != limit {
		t.Fatalf("acquired %d session slots, want %d", got, limit)
	}
	if slots.tryAcquire() {
		t.Fatal("slot guard allowed a session past its limit")
	}

	for range limit {
		slots.release()
	}
	if !slots.tryAcquire() {
		t.Fatal("released slots should be reusable")
	}
}
