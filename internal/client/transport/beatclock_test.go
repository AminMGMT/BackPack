package transport

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Until it has seen the server's rhythm the clock says what the old rule said.
func TestAnUntaughtClockKeepsTheKeepaliveRule(t *testing.T) {
	var b beatClock
	now := time.Now()
	b.beat(now)
	b.beat(now.Add(10 * time.Second))
	b.beat(now.Add(20 * time.Second)) // two gaps: not enough
	if got, want := b.deadline(75*time.Second), controlDeadline(75*time.Second); got != want {
		t.Fatalf("deadline = %s, want the keepalive rule's %s", got, want)
	}
}

// A server beating every ten seconds is given up on after thirty, not 112.
func TestATaughtClockGivesUpAfterThreeBeats(t *testing.T) {
	var b beatClock
	now := time.Now()
	for i := 0; i < 4; i++ {
		b.beat(now.Add(time.Duration(i) * 10 * time.Second))
	}
	if got := b.deadline(75 * time.Second); got != 30*time.Second {
		t.Fatalf("deadline = %s, want 30s", got)
	}
}

// A server that beats slowly — one from before the ten-second cap — is never
// held to less than the old rule.
func TestASlowServerIsNeverRushed(t *testing.T) {
	var b beatClock
	now := time.Now()
	for i := 0; i < 5; i++ {
		b.beat(now.Add(time.Duration(i) * 40 * time.Second))
	}
	if got, want := b.deadline(75*time.Second), controlDeadline(75*time.Second); got != want {
		t.Fatalf("deadline = %s, want the keepalive rule's %s", got, want)
	}
}

// The longest recent gap decides, and nothing goes below the floor: beats that
// arrive bunched after a stall must not make a lossy path look dead.
func TestTheLongestGapDecidesAndTheFloorHolds(t *testing.T) {
	var b beatClock
	now := time.Now()
	for _, at := range []time.Duration{0, 10, 11, 12, 20} {
		b.beat(now.Add(at * time.Second))
	}
	if got := b.deadline(75 * time.Second); got != 30*time.Second {
		t.Fatalf("deadline = %s, want 3 x the 10s gap", got)
	}
	var fast beatClock
	for i := 0; i < 5; i++ {
		fast.beat(now.Add(time.Duration(i) * time.Second))
	}
	if got := fast.deadline(75 * time.Second); got != livenessFloor {
		t.Fatalf("deadline = %s, want the floor %s", got, livenessFloor)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// A reconnect loop caused by a server heartbeat longer than this client's
// patience must say so (#45: a tunnel dropping every half minute with nothing
// in either log to explain it).
func TestASilenceBeforeAnyHeartbeatIsExplained(t *testing.T) {
	var b beatClock
	hint := b.explain(timeoutErr{}, 20*time.Second)
	if !strings.Contains(hint, "keepalive_period") || !strings.Contains(hint, "heartbeat") {
		t.Fatalf("hint = %q, want it to name the setting to change", hint)
	}
	b.beat(time.Now())
	if hint := b.explain(timeoutErr{}, 20*time.Second); strings.Contains(hint, "keepalive_period") {
		t.Fatalf("after a heartbeat, the hint still blames the setting: %q", hint)
	}
	if hint := b.explain(errors.New("EOF"), 20*time.Second); hint != "" {
		t.Fatalf("a non-timeout error was explained as silence: %q", hint)
	}
}

// Against a v1.8.2 server's schedule — seven quick beats doubling from 0.1 s,
// then one every ten seconds — the client has learnt the rhythm within 1.5
// seconds, gives up on
// a silent server after fifteen, and never mistakes the switch to the steady
// beat for a silence.
func TestTheWarmupTeachesTheClockAtOnce(t *testing.T) {
	warmup := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond,
		800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond, 6400 * time.Millisecond}
	var b beatClock
	now := time.Now()
	at := now
	for i := 0; i < 15; i++ {
		// Only heartbeats count, not the channel opening, so the first gap
		// the clock sees is the second warm-up one.
		gap := 10 * time.Second
		if i < len(warmup) {
			gap = warmup[i]
		}
		if limit := b.deadline(75 * time.Second); i > 0 && gap >= limit {
			t.Fatalf("beat %d arrives %s after the last, past the %s deadline", i, gap, limit)
		}
		at = at.Add(gap)
		b.beat(at)
		if at.Sub(now) < 1500*time.Millisecond {
			continue
		}
		if d := b.deadline(75 * time.Second); d > 30*time.Second {
			t.Fatalf("%s in, the deadline is still %s", at.Sub(now), d)
		}
	}
}

// A first beat a tenth of a second after the channel opens can only come from
// a server that warms up, so the clock trusts the floor at once — a server
// that dies a second into the connection is given up on after fifteen
// seconds, not 112.
func TestAWarmupBeatIsTrustedAtOnce(t *testing.T) {
	now := time.Now()
	b := newBeatClock(now)
	b.beat(now.Add(120 * time.Millisecond))
	if got := b.deadline(75 * time.Second); got != livenessFloor {
		t.Fatalf("deadline = %s after a warm-up beat, want %s", got, livenessFloor)
	}
}

// No older server beats inside a second of the channel opening — its shortest
// heartbeat is one second and its first beat is one interval in — so its first
// beat never counts as that evidence, however it is configured.
func TestAnOlderServersFirstBeatProvesNothing(t *testing.T) {
	for _, first := range []time.Duration{time.Second, 10 * time.Second, 40 * time.Second} {
		now := time.Now()
		b := newBeatClock(now)
		b.beat(now.Add(first))
		if got, want := b.deadline(75*time.Second), controlDeadline(75*time.Second); got != want {
			t.Fatalf("first beat after %s: deadline = %s, want the keepalive rule's %s", first, got, want)
		}
	}
}

// The quick start never makes the client stricter than its keepalive rule.
func TestAWarmupNeverShortensBelowAShortKeepalive(t *testing.T) {
	now := time.Now()
	b := newBeatClock(now)
	b.beat(now.Add(100 * time.Millisecond))
	if got, want := b.deadline(4*time.Second), controlDeadline(4*time.Second); got > want {
		t.Fatalf("deadline = %s, above the keepalive rule's %s", got, want)
	}
}
