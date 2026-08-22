package manage

import (
	"context"
	"strings"
	"testing"
	"time"
)

// End to end over the loopback: the receiver sinks, the sender measures, and
// the number that comes back is a real one.
func TestThroughputMeasuresARealTransfer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan error, 1)
	go func() { done <- ServeThroughput(ctx, "127.0.0.1") }()

	// Give the listener a moment to bind.
	for i := 0; i < 100; i++ {
		if r, err := MeasureThroughput(ctx, "127.0.0.1"); err == nil {
			if r.Bytes == 0 {
				t.Fatal("the measurement moved no bytes")
			}
			if r.Duration <= 0 {
				t.Fatal("the measurement took no time")
			}
			// Loopback is fast; the point is that the arithmetic produces a
			// plausible figure rather than a zero or an absurdity.
			if r.Mbps() <= 0 {
				t.Fatalf("rate = %v", r.Mbps())
			}
			if r.String() == "" {
				t.Fatal("the result does not render")
			}
			cancel()
			<-done
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("never reached the receiver")
}

// A run with nobody listening must say so in words an operator can act on,
// rather than reporting zero as if that were a measurement.
func TestThroughputSaysWhenNobodyIsListening(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	_, err := MeasureThroughput(ctx, "127.0.0.1")
	if err == nil {
		t.Fatal("measuring against nothing reported success")
	}
	// The operator has to be told what to do about it.
	if !strings.Contains(err.Error(), "is the receiver running") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

func TestThroughputArithmetic(t *testing.T) {
	// 100 Mbit in one second is 100 Mbit/s.
	r := ThroughputResult{Bytes: 12_500_000, Duration: time.Second}
	if got := r.Mbps(); got < 99.9 || got > 100.1 {
		t.Errorf("Mbps = %v, want 100", got)
	}
	// A zero duration must not divide by zero.
	if (ThroughputResult{Bytes: 1}).Mbps() != 0 {
		t.Error("a zero duration produced a rate")
	}
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[uint64]string{
		512:     "512 B",
		2048:    "2.0 KB",
		5 << 20: "5.0 MB",
		3 << 30: "3.00 GB",
	} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

// The receiver has to come back to the menu when it is interrupted.
//
// This is the regression test for the one action the screen tells you to take.
// It printed "Press Ctrl+C when the other end reports its result" and installed
// no handler, so the interrupt took Go's default and killed the whole CLI —
// menu, tunnel list and all. What the sink does on cancellation is the half of
// that which can be tested here: it must stop promptly and report no error, so
// the caller returns to the menu rather than showing a failure.
func TestTheSinkStopsWhenItIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- ServeThroughput(ctx, "127.0.0.1") }()

	// Give it a moment to bind, then interrupt it the way the screen does.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("an interrupted receiver reported an error, so the screen "+
				"would show a failure instead of returning quietly: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the receiver did not stop when cancelled; Ctrl+C would have to " +
			"kill the process to end it, which is the bug")
	}
}

// Cancelling has to release the port, or a second run cannot bind.
func TestTheSinkReleasesItsPortOnTheWayOut(t *testing.T) {
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- ServeThroughput(ctx, "127.0.0.1") }()
		time.Sleep(200 * time.Millisecond)
		cancel()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("run %d: %v", i+1, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("run %d did not stop", i+1)
		}
	}
}
