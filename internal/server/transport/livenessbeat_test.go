package transport

import (
	"testing"
	"time"
)

func TestTheControlHeartbeatIsNeverSlowerThanTenSeconds(t *testing.T) {
	for configured, want := range map[time.Duration]time.Duration{
		0:                maxLivenessBeat,
		5 * time.Second:  5 * time.Second,
		10 * time.Second: 10 * time.Second,
		40 * time.Second: maxLivenessBeat,
	} {
		if got := livenessBeat(configured); got != want {
			t.Errorf("livenessBeat(%s) = %s, want %s", configured, got, want)
		}
	}
}

// The schedule opens with the warm-up and settles on the steady beat; a
// heartbeat configured shorter than a warm-up gap is never slowed to it.
func TestTheHeartbeatWarmsUpThenSettles(t *testing.T) {
	steady := livenessBeat(40 * time.Second)
	var at time.Duration
	for i := 0; i < len(livenessWarmup); i++ {
		at += livenessGap(i, steady)
	}
	if at > 13*time.Second {
		t.Fatalf("the warm-up takes %s", at)
	}
	if got := livenessGap(len(livenessWarmup), steady); got != steady {
		t.Fatalf("after the warm-up the gap is %s, want %s", got, steady)
	}
	for i := 0; i < 10; i++ {
		if got := livenessGap(i, time.Second); got > time.Second {
			t.Fatalf("a one-second heartbeat waited %s before beat %d", got, i)
		}
	}
}

// The ticker itself delivers the first beats quickly, and Stop ends it.
func TestTheLivenessTickerBeatsEarly(t *testing.T) {
	tk := newLivenessTicker(40 * time.Second)
	defer tk.Stop()
	began := time.Now()
	for i := 0; i < 3; i++ {
		select {
		case <-tk.C:
		case <-time.After(3 * time.Second):
			t.Fatalf("beat %d did not come", i)
		}
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Fatalf("three beats took %s", took)
	}
	tk.Stop()
	tk.Stop() // twice is fine
}
