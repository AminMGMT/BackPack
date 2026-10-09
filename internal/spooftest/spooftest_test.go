package spooftest

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestExpandSpec(t *testing.T) {
	// single
	if ips, err := ExpandSpec("8.8.4.4"); err != nil || len(ips) != 1 || ips[0].String() != "8.8.4.4" {
		t.Fatalf("single: %v %v", ips, err)
	}
	// CIDR /30 = 4 addresses
	if ips, err := ExpandSpec("192.0.2.0/30"); err != nil || len(ips) != 4 {
		t.Fatalf("cidr: %d %v", len(ips), err)
	}
	// range inclusive
	ips, err := ExpandSpec("10.0.0.1-10.0.0.3")
	if err != nil || len(ips) != 3 || ips[2].String() != "10.0.0.3" {
		t.Fatalf("range: %v %v", ips, err)
	}
	// invalid
	if _, err := ExpandSpec("not-an-ip"); err == nil {
		t.Fatal("invalid spec should error")
	}
}

func TestSpoofProbesRespectCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunSender(SenderConfig{Context: ctx, TargetIP: net.ParseIP("127.0.0.1")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sender opened a raw socket: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := RunReceiver(ReceiverConfig{Context: ctx, Window: time.Minute})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("receiver waited for its window after cancellation: %v", err)
	}
}

func TestExpandListDedup(t *testing.T) {
	// The single overlaps the CIDR; the union must not duplicate it.
	ips, err := ExpandList("192.0.2.0/30, 192.0.2.1, 8.8.8.8")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, ip := range ips {
		seen[ip.String()]++
	}
	if seen["192.0.2.1"] != 1 {
		t.Fatalf("192.0.2.1 duplicated: %v", seen)
	}
	if seen["8.8.8.8"] != 1 || len(ips) != 5 {
		t.Fatalf("unexpected union: %v (%d)", seen, len(ips))
	}
}

func TestProbeRoundTrip(t *testing.T) {
	magic := Magic("shared-token")
	seq, ok := decodeProbe(magic, encodeProbe(magic, 42))
	if !ok || seq != 42 {
		t.Fatalf("probe did not round-trip: %d %v", seq, ok)
	}
	// Wrong magic (different token) is rejected.
	if _, ok := decodeProbe(Magic("other"), encodeProbe(magic, 1)); ok {
		t.Fatal("a probe with the wrong magic was accepted")
	}
	// A short buffer is rejected, not sliced out of bounds.
	if _, ok := decodeProbe(magic, []byte{1, 2, 3}); ok {
		t.Fatal("a short probe was accepted")
	}
}

func TestLossAndPassing(t *testing.T) {
	results := []Result{
		{IP: net.ParseIP("1.1.1.1"), Arrived: 5, Attempts: 5}, // 0%
		{IP: net.ParseIP("2.2.2.2"), Arrived: 4, Attempts: 5}, // 20%
		{IP: net.ParseIP("3.3.3.3"), Arrived: 0, Attempts: 5}, // 100%
	}
	if got := results[1].LossPercent(); got != 20 {
		t.Fatalf("loss want 20, got %v", got)
	}
	passing := Passing(results, 20)
	if len(passing) != 2 {
		t.Fatalf("want 2 passing at ≤20%%, got %d", len(passing))
	}
	// A capped-over arrival (duplicate seqs beyond attempts) must not go negative.
	over := Result{IP: net.ParseIP("4.4.4.4"), Arrived: 7, Attempts: 5}
	if over.LossPercent() != 0 {
		t.Fatalf("over-arrival loss should clamp to 0, got %v", over.LossPercent())
	}
}
