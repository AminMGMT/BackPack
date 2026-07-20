package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/sysstat"
)

// The value of an alert system is entirely in when it stays quiet. These tests
// exercise the quiet cases as hard as the firing ones.

func testConfig() AlertConfig {
	return AlertConfig{
		Enabled:         true,
		CPUPercent:      80,
		MemPercent:      85,
		DiskPercent:     90,
		TunnelDown:      true,
		CheckSeconds:    60,
		CooldownMinutes: 30,
	}
}

func cpuAt(pct float64) sysstat.Snapshot {
	return sysstat.Snapshot{CPUPercent: pct, CPUCores: 4}
}

// seeded returns a watcher that has already recorded the given tunnel states,
// so transitions are reported rather than swallowed by the first-pass seeding.
func seeded(tunnels map[string]bool) *watcher {
	w := newWatcher()
	w.tunnelUp = tunnels
	w.seeded = true
	return w
}

func TestAlertFiresWhenThresholdCrossed(t *testing.T) {
	w := seeded(nil)
	now := time.Now()

	msgs := w.checkAt(testConfig(), cpuAt(84), nil, now)
	if len(msgs) != 1 {
		t.Fatalf("crossing the threshold should produce exactly one alert, got %d: %v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0], "84") || !strings.Contains(msgs[0], "Processor") {
		t.Errorf("the alert should name the reading and what it measured, got %q", msgs[0])
	}
}

func TestBelowThresholdIsSilent(t *testing.T) {
	w := seeded(nil)
	if msgs := w.checkAt(testConfig(), cpuAt(79.9), nil, time.Now()); len(msgs) != 0 {
		t.Fatalf("a reading below the threshold must be silent, got %v", msgs)
	}
}

// A condition that persists must not produce a message on every check — this is
// the difference between a useful bot and one that gets muted.
func TestSustainedConditionRespectsCooldown(t *testing.T) {
	w := seeded(nil)
	cfg := testConfig()
	start := time.Now()

	if msgs := w.checkAt(cfg, cpuAt(95), nil, start); len(msgs) != 1 {
		t.Fatalf("want the initial alert, got %v", msgs)
	}
	// Ten more checks over the next 10 minutes, still pinned, still inside the
	// 30-minute cooldown.
	for i := 1; i <= 10; i++ {
		at := start.Add(time.Duration(i) * time.Minute)
		if msgs := w.checkAt(cfg, cpuAt(95), nil, at); len(msgs) != 0 {
			t.Fatalf("check at +%dm should have been silent, got %v", i, msgs)
		}
	}
	// Past the cooldown, one reminder.
	msgs := w.checkAt(cfg, cpuAt(95), nil, start.Add(31*time.Minute))
	if len(msgs) != 1 {
		t.Fatalf("want one reminder after the cooldown, got %v", msgs)
	}
	if !strings.Contains(msgs[0], "still") {
		t.Errorf("a reminder should read as a reminder, got %q", msgs[0])
	}
}

// A value hovering on the threshold is the classic source of alert spam.
func TestHoveringOnThresholdDoesNotFlap(t *testing.T) {
	w := seeded(nil)
	cfg := testConfig() // threshold 80, clears below 75
	now := time.Now()

	msgs := w.checkAt(cfg, cpuAt(80.5), nil, now)
	if len(msgs) != 1 {
		t.Fatalf("want the initial alert, got %v", msgs)
	}
	// Oscillate either side of the line. None of this should say anything: it
	// is neither a new crossing nor a real recovery.
	for i, pct := range []float64{79, 81, 78, 80.2, 77, 79.5} {
		at := now.Add(time.Duration(i+1) * time.Minute)
		if msgs := w.checkAt(cfg, cpuAt(pct), nil, at); len(msgs) != 0 {
			t.Fatalf("hovering at %.1f%% produced noise: %v", pct, msgs)
		}
	}
	// Genuinely recovered: below the clear margin.
	msgs = w.checkAt(cfg, cpuAt(60), nil, now.Add(10*time.Minute))
	if len(msgs) != 1 || !strings.Contains(msgs[0], "normal") {
		t.Fatalf("want a single recovery message, got %v", msgs)
	}
	// And recovery is reported once, not on every subsequent check.
	if msgs := w.checkAt(cfg, cpuAt(55), nil, now.Add(11*time.Minute)); len(msgs) != 0 {
		t.Fatalf("recovery should be reported once, got %v", msgs)
	}
}

func TestZeroThresholdDisablesTheCheck(t *testing.T) {
	cfg := testConfig()
	cfg.CPUPercent = 0
	w := seeded(nil)

	if msgs := w.checkAt(cfg, cpuAt(100), nil, time.Now()); len(msgs) != 0 {
		t.Fatalf("a zero threshold must disable the check, got %v", msgs)
	}
}

// Restarting the panel must not announce every tunnel that happens to be up.
func TestFirstPassSeedsWithoutAnnouncing(t *testing.T) {
	w := newWatcher() // not seeded
	tunnels := map[string]bool{"iran-tcp": true, "iran-kcp": false}

	if msgs := w.checkAt(testConfig(), cpuAt(5), tunnels, time.Now()); len(msgs) != 0 {
		t.Fatalf("the first pass should only record state, got %v", msgs)
	}
	// A subsequent change is reported.
	tunnels2 := map[string]bool{"iran-tcp": false, "iran-kcp": false}
	msgs := w.checkAt(testConfig(), cpuAt(5), tunnels2, time.Now())
	if len(msgs) != 1 || !strings.Contains(msgs[0], "iran-tcp") {
		t.Fatalf("want a down alert for iran-tcp, got %v", msgs)
	}
}

func TestTunnelTransitions(t *testing.T) {
	cases := []struct {
		name  string
		from  map[string]bool
		to    map[string]bool
		want  int
		match string
	}{
		{"goes down", map[string]bool{"a": true}, map[string]bool{"a": false}, 1, "went down"},
		{"comes back", map[string]bool{"a": false}, map[string]bool{"a": true}, 1, "back up"},
		{"stays up", map[string]bool{"a": true}, map[string]bool{"a": true}, 0, ""},
		{"stays down", map[string]bool{"a": false}, map[string]bool{"a": false}, 0, ""},
		{"new and down", map[string]bool{}, map[string]bool{"b": false}, 1, "is down"},
		{"new and up", map[string]bool{}, map[string]bool{"b": true}, 0, ""},
		{"removed", map[string]bool{"a": true}, map[string]bool{}, 1, "no longer exists"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := seeded(tc.from)
			msgs := w.checkAt(testConfig(), cpuAt(5), tc.to, time.Now())
			if len(msgs) != tc.want {
				t.Fatalf("want %d message(s), got %d: %v", tc.want, len(msgs), msgs)
			}
			if tc.match != "" && !strings.Contains(msgs[0], tc.match) {
				t.Errorf("want a message containing %q, got %q", tc.match, msgs[0])
			}
		})
	}
}

func TestTunnelAlertsCanBeSwitchedOff(t *testing.T) {
	cfg := testConfig()
	cfg.TunnelDown = false
	w := seeded(map[string]bool{"a": true})

	if msgs := w.checkAt(cfg, cpuAt(5), map[string]bool{"a": false}, time.Now()); len(msgs) != 0 {
		t.Fatalf("tunnel alerts are off, got %v", msgs)
	}
}

// Each resource tracks its own state, so a busy processor cannot mask a full
// disk or suppress its alert.
func TestResourcesAreIndependent(t *testing.T) {
	w := seeded(nil)
	cfg := testConfig()
	now := time.Now()

	s := sysstat.Snapshot{CPUPercent: 95, MemPercent: 10, DiskPercent: 95}
	msgs := w.checkAt(cfg, s, nil, now)
	if len(msgs) != 2 {
		t.Fatalf("want separate alerts for processor and disk, got %d: %v", len(msgs), msgs)
	}

	// Processor recovers, disk does not: exactly one recovery, no disk noise.
	s2 := sysstat.Snapshot{CPUPercent: 20, MemPercent: 10, DiskPercent: 95}
	msgs = w.checkAt(cfg, s2, nil, now.Add(time.Minute))
	if len(msgs) != 1 || !strings.Contains(msgs[0], "Processor") || !strings.Contains(msgs[0], "normal") {
		t.Fatalf("want only a processor recovery, got %v", msgs)
	}
}

// A config from a build that predates alerts decodes as a zero value. It must
// not turn into a watcher that samples every zero seconds.
func TestNormaliseFillsMissingIntervals(t *testing.T) {
	got := AlertConfig{Enabled: true, CPUPercent: 80}.normalise()
	if got.CheckSeconds != defaultCheckSeconds {
		t.Errorf("CheckSeconds = %d, want the default %d", got.CheckSeconds, defaultCheckSeconds)
	}
	if got.CooldownMinutes != defaultCooldownMinutes {
		t.Errorf("CooldownMinutes = %d, want the default %d", got.CooldownMinutes, defaultCooldownMinutes)
	}
}

func TestSummaryReportsOffState(t *testing.T) {
	if s := (AlertConfig{Enabled: false}).Summary(); !strings.Contains(s, "off") {
		t.Errorf("a disabled config should say so, got %q", s)
	}
	s := DefaultAlerts().Summary()
	for _, want := range []string{"Processor", "Memory", "Disk", "85", "90"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary is missing %q:\n%s", want, s)
		}
	}
}

func TestCommandParsing(t *testing.T) {
	cases := map[string]string{
		"/status":             "status",
		"/status@backpackbot": "status",
		"/System":             "system",
		"/metrics extra arg":  "metrics",
		"  /help  ":           "help",
		"hello":               "",
		"":                    "",
		"/":                   "",
	}
	for in, want := range cases {
		if got := command(in); got != want {
			t.Errorf("command(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBarStaysInBounds(t *testing.T) {
	for _, pct := range []float64{-10, 0, 50, 99.99, 100, 150} {
		got := bar(pct)
		if n := len([]rune(got)); n != 12 { // 10 segments plus the brackets
			t.Errorf("bar(%v) = %q, which is %d runes wide, want 12", pct, got, n)
		}
	}
}
