package schedule

import (
	"testing"
	"time"
)

// The panel counts down to this, so it has to be the minute cron fires, on
// the server's own clock — a half-hour zone included.
func TestNextRunIsWhenCronFires(t *testing.T) {
	tehran := time.FixedZone("IRST", 3*3600+1800)
	at := func(d, h, m int) time.Time { return time.Date(2026, 9, d, h, m, 0, 0, tehran) }
	cases := []struct {
		hours int
		now   time.Time
		want  time.Time
	}{
		{0, at(10, 5, 0), time.Time{}},
		{6, at(10, 5, 10), at(10, 6, 0)},
		{6, at(10, 6, 0), at(10, 12, 0)},
		{5, at(10, 21, 40), at(11, 0, 0)}, // steps restart at midnight
		{1, at(10, 7, 59), at(10, 8, 0)},
		{24, at(10, 7, 0), at(11, 0, 0)},
		{48, at(10, 7, 0), at(11, 0, 0)}, // days 1, 3, 5 … 11
		{48, at(11, 0, 0), at(13, 0, 0)},
		{36, at(10, 7, 0), at(11, 0, 0)},                               // rounded to a day, as cron has it
		{72, at(30, 1, 0), time.Date(2026, 10, 1, 0, 0, 0, 0, tehran)}, // month restarts the step
	}
	for _, c := range cases {
		if got := NextRun(c.hours, c.now); !got.Equal(c.want) {
			t.Errorf("every %dh from %s: got %s, want %s", c.hours, c.now.Format("02 15:04"), got, c.want)
		}
	}
}
