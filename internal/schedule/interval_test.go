package schedule

import "testing"

// What is written has to be what reads back, or every screen lies about the
// schedule while cron runs it correctly.
//
// HourlySpec writes "0 0 */N * *" for anything over a day and GetIntervalHours
// matched only the two other forms, so every interval above 24 hours read back
// as 0 — which the menu, the Telegram bot and the web panel all render as
// "disabled". The job was installed, cron had it, the tunnels were refreshed on
// it, and nothing on the machine would admit it existed. An operator who set 48
// hours saw it off the next time they looked, set it again, and got the same
// job written twice.
func TestEveryIntervalReadsBackAsItWasWritten(t *testing.T) {
	for _, hours := range []int{1, 2, 3, 4, 6, 8, 12, 24, 48, 72, 168} {
		spec := HourlySpec(hours)
		if spec == "" {
			t.Errorf("%dh produced no schedule", hours)
			continue
		}
		got := intervalFromLine(spec + " /usr/local/bin/backpack --restart-all # bp-auto-refresh")
		if got != hours {
			t.Errorf("%dh was written as %q and reads back as %dh", hours, spec, got)
		}
	}
}

// Anything cron cannot express is rounded, and the rounding is something a
// caller can ask about rather than something it finds out later.
func TestAnIntervalCronCannotExpressIsReportedAsWhatItBecomes(t *testing.T) {
	for _, tc := range []struct{ asked, want int }{
		{0, 0}, {1, 1}, {23, 23}, {24, 24},
		{25, 24}, {36, 24}, {47, 24}, {48, 48}, {70, 48}, {72, 72},
	} {
		if got := EffectiveHours(tc.asked); got != tc.want {
			t.Errorf("asking for %dh gives %dh, want %dh", tc.asked, got, tc.want)
		}
		// And what it becomes is what the crontab reads back as.
		if tc.asked > 0 {
			line := HourlySpec(tc.asked) + " cmd # marker"
			if got := intervalFromLine(line); got != tc.want {
				t.Errorf("asking for %dh installed %q, which reads back as %dh, want %dh",
					tc.asked, HourlySpec(tc.asked), got, tc.want)
			}
		}
	}
}

// Nothing off the schedule is read as a schedule.
func TestALineThatIsNotAnIntervalReadsAsDisabled(t *testing.T) {
	for _, line := range []string{
		"30 4 * * 1 cmd # marker", // a weekly job at a fixed time
		"@reboot cmd # marker",
		"",
	} {
		if got := intervalFromLine(line); got != 0 {
			t.Errorf("%q read back as %dh", line, got)
		}
	}
}
