package manage

import (
	"testing"
	"time"
)

// quality builds a PathQuality with the fields the recommenders actually read.
func quality(avg, max, jitter time.Duration, sent, received int) PathQuality {
	return PathQuality{
		Target: "1.2.3.4:443", Sent: sent, Received: received,
		Min: avg / 2, Avg: avg, Max: max, Jitter: jitter,
	}
}

func TestRecommendTransportCleanLink(t *testing.T) {
	// Steady, lossless link: nothing to repair, so plain multiplexed TCP.
	q := quality(40*time.Millisecond, 45*time.Millisecond, 3*time.Millisecond, 12, 12)
	got := RecommendTransport(q, "tcp")
	if got.Transport != "tcpmux" {
		t.Fatalf("Transport = %q, want tcpmux on a clean link", got.Transport)
	}
	if len(got.Why) == 0 {
		t.Fatal("a recommendation must explain itself")
	}
}

func TestRecommendTransportLossyPicksKCP(t *testing.T) {
	// 3 of 12 probes lost is 25% — squarely KCP territory.
	q := quality(90*time.Millisecond, 200*time.Millisecond, 30*time.Millisecond, 12, 9)
	got := RecommendTransport(q, "tcp")
	if got.Transport != "kcp" {
		t.Fatalf("Transport = %q, want kcp on a lossy link", got.Transport)
	}
	if got.Preset != PresetAggressive {
		t.Fatalf("Preset = %q, want aggressive at this loss level", got.Preset)
	}
	// A UDP recommendation must always carry the UDP-throttling caveat.
	if len(got.Caveats) == 0 {
		t.Fatal("recommending a UDP transport without a caveat would be misleading")
	}
}

func TestRecommendTransportJitteryPicksMux(t *testing.T) {
	// No loss, but latency swings wildly — a shaped path.
	q := quality(60*time.Millisecond, 300*time.Millisecond, 40*time.Millisecond, 12, 12)
	got := RecommendTransport(q, "tcp")
	if got.Transport != "tcpmux" {
		t.Fatalf("Transport = %q, want tcpmux on a jittery link", got.Transport)
	}
}

func TestRecommendTransportUnreachablePicksCamouflage(t *testing.T) {
	q := quality(0, 0, 0, 12, 0)
	got := RecommendTransport(q, "tcp")
	if got.Transport != "wss" {
		t.Fatalf("Transport = %q, want wss when nothing answers", got.Transport)
	}
	// It must not let the user conclude "filtered" when the server may be down.
	if len(got.Caveats) == 0 {
		t.Fatal("an unreachable verdict must warn that the server may simply be down")
	}
}

func TestRecommendKeepAliveStaysAboveWorstRTT(t *testing.T) {
	// A heartbeat shorter than the worst round trip would declare a healthy
	// peer dead, so this is the property that actually matters.
	q := quality(400*time.Millisecond, 3*time.Second, 500*time.Millisecond, 12, 12)
	plan := RecommendKeepAlive(q)
	if time.Duration(plan.Heartbeat)*time.Second <= q.Max {
		t.Fatalf("Heartbeat = %ds, must exceed the worst round trip of %v", plan.Heartbeat, q.Max)
	}
	if plan.KeepAlive <= plan.Heartbeat {
		t.Fatalf("KeepAlive %ds must be longer than Heartbeat %ds", plan.KeepAlive, plan.Heartbeat)
	}
}

func TestRecommendKeepAliveBounds(t *testing.T) {
	// A very fast link must not produce a hammering heartbeat.
	fast := quality(2*time.Millisecond, 3*time.Millisecond, time.Millisecond, 12, 12)
	if got := RecommendKeepAlive(fast).Heartbeat; got < 10 {
		t.Fatalf("Heartbeat = %ds, must not drop below 10s even on a LAN", got)
	}
	// A terrible link must not produce a heartbeat so long a drop goes unnoticed.
	slow := quality(5*time.Second, 30*time.Second, 10*time.Second, 12, 12)
	if got := RecommendKeepAlive(slow).Heartbeat; got > 60 {
		t.Fatalf("Heartbeat = %ds, must be capped at 60s", got)
	}
}

func TestRecommendKeepAliveUnmeasurableKeepsDefaults(t *testing.T) {
	plan := RecommendKeepAlive(quality(0, 0, 0, 12, 0))
	if plan.KeepAlive != 75 || plan.Heartbeat != 40 {
		t.Fatalf("got %ds/%ds, want the 75/40 defaults when the link is unmeasurable",
			plan.KeepAlive, plan.Heartbeat)
	}
}

func TestLossPercent(t *testing.T) {
	if got := quality(0, 0, 0, 12, 9).LossPercent(); got != 25 {
		t.Fatalf("LossPercent = %v, want 25", got)
	}
	if got := (PathQuality{}).LossPercent(); got != 0 {
		t.Fatalf("LossPercent on an empty probe = %v, want 0", got)
	}
}
