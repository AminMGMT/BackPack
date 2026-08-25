package transport

import (
	"testing"
	"time"
)

// What the control channel costs when nothing watches it.
//
// TcpDialerVia enables TCP keepalive with Count: 9 and Idle: 0, and a zero Idle
// means Go's own default of 15 seconds. With the shipped keepalive_period the
// kernel therefore waits a quarter of an hour's worth of probes before it
// reports the peer gone — and until it does, the socket stays ESTABLISHED, so
// the watchdog reads the tunnel as healthy for every second of it.
//
// This is not a hypothetical. It is the reported failure: a tunnel died about
// eighty seconds after it came up and was reported healthy until the kernel
// gave up eleven and a half minutes later.
const (
	defaultKeepAlivePeriod = 75 * time.Second
	tcpKeepAliveIdle       = 15 * time.Second // Go's default for a zero Idle
	tcpKeepAliveCount      = 9                // net.KeepAliveConfig in TcpDialerVia
	tcpGiveUpBudget        = tcpKeepAliveIdle + tcpKeepAliveCount*defaultKeepAlivePeriod
)

func TestTheControlChannelIsGivenUpOnLongBeforeTCPWould(t *testing.T) {
	got := controlDeadline(defaultKeepAlivePeriod)
	if got >= tcpGiveUpBudget {
		t.Fatalf("the control channel deadline is %s, and TCP gives up by itself after %s — "+
			"the deadline buys nothing", got, tcpGiveUpBudget)
	}
	if got > 2*time.Minute {
		t.Errorf("a dead tunnel takes %s to notice on the shipped defaults; the point of "+
			"the deadline is to keep that near two minutes", got)
	}
}

// The opposite failure, and the more damaging one: tearing down a tunnel that
// is alive. keepalive_period is written as twice the heartbeat, so a deadline
// of one keepalive period is exactly two heartbeat intervals — no room at all
// for a heartbeat that is merely late or lost, which on the lossy paths this
// tunnel is built for is ordinary rather than exceptional.
func TestTheDeadlineSurvivesALostHeartbeat(t *testing.T) {
	for _, keepAlive := range []time.Duration{
		20 * time.Second, // the tuner's tightest: a 10s heartbeat
		defaultKeepAlivePeriod,
		120 * time.Second, // the tuner's loosest: a 60s heartbeat
	} {
		heartbeat := keepAlive / 2
		got := controlDeadline(keepAlive)
		if got < 3*heartbeat {
			t.Errorf("keepalive %s implies a %s heartbeat, and a deadline of %s expires "+
				"before the second heartbeat after a lost one arrives",
				keepAlive, heartbeat, got)
		}
	}
}

// Tightening the timers has to tighten the detection, or Link Test's offer to
// tune them means nothing.
func TestTunedTimersNoticeSooner(t *testing.T) {
	tuned := controlDeadline(20 * time.Second)
	shipped := controlDeadline(defaultKeepAlivePeriod)
	if tuned >= shipped {
		t.Fatalf("tuned timers give a %s deadline against %s on the defaults", tuned, shipped)
	}
}

// A hand-written keepalive_period of a second or two must not turn the tunnel
// into a restart loop, and no configuration at all must still produce a
// deadline — the whole point is that this path is never left unbounded.
func TestTheDeadlineIsAlwaysUsable(t *testing.T) {
	if got := controlDeadline(time.Second); got < controlIdleFloor {
		t.Errorf("a 1s keepalive gives a %s deadline, below the %s floor", got, controlIdleFloor)
	}
	if got := controlDeadline(0); got != controlIdleFallback {
		t.Errorf("no keepalive gives a %s deadline, want the %s fallback", got, controlIdleFallback)
	}
	if got := controlDeadline(-time.Second); got != controlIdleFallback {
		t.Errorf("a negative keepalive gives a %s deadline, want the %s fallback", got, controlIdleFallback)
	}
}
