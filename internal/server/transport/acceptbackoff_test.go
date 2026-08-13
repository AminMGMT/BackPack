package transport

import (
	"context"
	"testing"
	"time"
)

// A listener that is permanently broken returns an error instantly and forever.
// Before the backoff the loop retried with no pause at all, so this many
// failures took no measurable time and burned a core doing it. The pause is what
// makes that impossible; the exact schedule matters less than that it exists.
func TestAcceptBackoffStopsTheSpin(t *testing.T) {
	var b acceptBackoff
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < 8; i++ {
		if !b.fail(ctx) {
			t.Fatal("fail reported a cancelled context on a live one")
		}
	}
	elapsed := time.Since(start)

	// 5+10+20+40+80+100+100+100 = 455ms of intended pause. Assert well under it
	// to stay reliable on a loaded CI box, but far above the ~0ns a spin costs.
	if elapsed < 200*time.Millisecond {
		t.Fatalf("eight consecutive failures paused only %v — the loop is still spinning", elapsed)
	}
}

// A successful accept must clear the backoff, or one bad connection would slow
// every good one behind it.
func TestAcceptBackoffResetsOnSuccess(t *testing.T) {
	var b acceptBackoff
	ctx := context.Background()

	for i := 0; i < 6; i++ {
		b.fail(ctx) // climb to the ceiling
	}
	b.ok()

	start := time.Now()
	b.fail(ctx)
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("after a success the next failure waited %v — the backoff did not reset", elapsed)
	}
}

// Cancellation must interrupt the pause, so a shutdown is not delayed by it.
func TestAcceptBackoffReturnsOnCancel(t *testing.T) {
	var b acceptBackoff
	for i := 0; i < 6; i++ {
		b.fail(context.Background()) // climb to the 100ms ceiling
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if b.fail(ctx) {
		t.Fatal("fail reported success though the context was cancelled")
	}
	if elapsed := time.Since(start); elapsed > 90*time.Millisecond {
		t.Fatalf("cancellation took %v to interrupt the pause", elapsed)
	}
}
