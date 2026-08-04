package transport

import (
	"context"
	"testing"
	"time"
)

// The first wait is near the floor, the gap grows, and it caps at the ceiling.
func TestBackoffGrowsAndCaps(t *testing.T) {
	b := newBackoff(time.Second) // min 1s, max 30s

	// Attempt 0: at least the floor, at most floor + 20% jitter.
	if d := b.duration(); d < time.Second || d > time.Duration(1.2*float64(time.Second)) {
		t.Fatalf("first duration = %v, want ~1s", d)
	}

	// Drive the attempt counter up; the duration must approach and hold the cap.
	for i := 0; i < 12; i++ {
		b.attempt++
	}
	d := b.duration()
	if d < time.Duration(0.8*float64(30*time.Second)) || d > time.Duration(1.2*float64(30*time.Second)) {
		t.Fatalf("capped duration = %v, want ~30s", d)
	}
}

// Wait returns promptly (false) when the context is cancelled.
func TestBackoffWaitCancels(t *testing.T) {
	b := newBackoff(10 * time.Second) // long enough that only cancellation ends it
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if b.Wait(ctx) {
		t.Fatal("Wait should return false on a cancelled context")
	}
	if time.Since(start) > time.Second {
		t.Fatal("Wait did not return promptly on cancellation")
	}
}

// A zero/negative interval falls back to a sane floor rather than busy-looping.
func TestBackoffFloor(t *testing.T) {
	b := newBackoff(0)
	if b.min != time.Second {
		t.Fatalf("min = %v, want 1s fallback", b.min)
	}
}
