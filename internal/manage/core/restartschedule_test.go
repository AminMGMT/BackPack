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
	if EffectiveRestartHours(36) != 24 || EffectiveRestartHours(0) != 0 {
		t.Error("EffectiveRestartHours")
	}
}

// Hours below a day are counted from midnight UTC, so an interval that does not
// divide 24 fired twice close together at midnight ("every 23 hours" at 23:00
// and 00:00). Each one becomes the largest divisor of 24 at or under it, and
// every interval the calendar then gets is the same length.
func TestARestartIntervalUnderADayDividesTheDay(t *testing.T) {
	want := map[int]int{1: 1, 2: 2, 3: 3, 4: 4, 5: 4, 6: 6, 7: 6, 8: 8, 9: 8, 11: 8, 12: 12, 13: 12, 23: 12, 24: 24}
	for in, out := range want {
		if got := EffectiveRestartHours(in); got != out {
			t.Errorf("%d hours → %d, want %d", in, got, out)
		}
	}
	for h := 1; h < 24; h++ {
		if e := EffectiveRestartHours(h); 24%e != 0 {
			t.Errorf("%d hours → %d, which does not divide a day", h, e)
		}
	}
}
