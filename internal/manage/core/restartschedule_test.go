package core

import (
	"strings"
	"testing"
)

// Both servers are told the same hours and minute and must fire at the same
// instant wherever they are, so the calendar is written in UTC; and what the
// setup link hands on is read back from the timer, not remembered elsewhere.
func TestTheRestartScheduleIsUTCAndReadsBack(t *testing.T) {
	for _, tc := range []struct {
		hours, minute int
		cal           string
	}{
		{6, 17, "*-*-* 00/6:17:00 UTC"},
		{1, 0, "*-*-* 00/1:00:00 UTC"},
		{24, 5, "*-*-* 00:05:00 UTC"},
		{48, 59, "*-*-01/2 00:59:00 UTC"},
	} {
		cal := restartCalendar(tc.hours, tc.minute)
		if cal != tc.cal {
			t.Errorf("%dh:%d → %q, want %q", tc.hours, tc.minute, cal, tc.cal)
		}
		m := restartCalendarRe.FindStringSubmatch("OnCalendar=" + cal)
		if m == nil {
			t.Errorf("%q does not read back", cal)
		}
	}
	if !strings.HasPrefix(scheduledRestartUnit("k1", "timer"), "backpack-restart-") {
		t.Error("the timer must not be named like a tunnel's own unit")
	}
	if EffectiveRestartHours(36) != 24 || EffectiveRestartHours(0) != 0 || EffectiveRestartHours(7) != 7 {
		t.Error("EffectiveRestartHours")
	}
}
