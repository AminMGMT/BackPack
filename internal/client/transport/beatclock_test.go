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
