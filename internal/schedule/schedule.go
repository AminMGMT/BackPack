// Package schedule manages recurring backpack jobs via the system crontab
// (auto-refresh of tunnels and periodic Telegram reports).
package schedule

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/backpack/backpack/internal/app"
)

// EffectiveHours returns the interval a request for `hours` actually produces.
//
// cron has no way to say "every 36 hours", so anything above a day is rounded
// down to a whole number of days — which is a fine thing for a scheduler to do
// and a bad thing for it to do quietly. Asking for 36 gave a job that ran every
// 24, and every screen went on reporting 36 because it was reading back the
// number it had been given rather than the one in the crontab.
//
// Callers show this rather than what was typed.
func EffectiveHours(hours int) int {
	switch {
	case hours <= 0:
		return 0
	case hours <= 24:
		return hours
	default:
		return (hours / 24) * 24
	}
}

// HourlySpec returns a cron schedule string that fires every `hours` hours.
func HourlySpec(hours int) string {
	switch {
	case hours <= 0:
		return ""
	case hours == 1:
		return "0 * * * *"
	case hours < 24:
		return fmt.Sprintf("0 */%d * * *", hours)
	case hours == 24:
		return "0 0 * * *"
	default:
		days := hours / 24
		return fmt.Sprintf("0 0 */%d * *", days)
	}
}

// readCrontab returns the current crontab lines, and tells "there is no
// crontab" apart from "the crontab could not be read".
//
// It used to answer both with nil, and SetCron then wrote a crontab built from
// that nil — which is a crontab containing one line, ours. So any transient
// failure of `crontab -l` turned installing a backpack job into deleting every
// other job on the machine: the operator's backups, their certificate renewals,
// their own scripts, gone, with the program reporting success.
//
// "No crontab for <user>" is the one failure that genuinely means empty, and
// every cron implementation in use says those words. Anything else is a read
// that did not happen, and a write built on it is a guess about what was there.
func readCrontab() ([]string, error) {
	out, err := exec.Command("crontab", "-l").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && noCrontabYet(ee.Stderr) {
			return nil, nil
		}
		return nil, fmt.Errorf("could not read the current crontab: %w", err)
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// noCrontabYet reports whether cron's complaint is the ordinary "this user has
// no crontab", which is an empty crontab and not a failure.
func noCrontabYet(stderr []byte) bool {
	return strings.Contains(strings.ToLower(string(stderr)), "no crontab")
}

// writeCrontab installs the given lines as the crontab.
func writeCrontab(lines []string) error {
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	return cmd.Run()
}

// SetCron installs (or replaces) a marked cron job. An empty spec removes it.
// Each job is tagged with a trailing `# <marker>` comment for identification.
func SetCron(marker, spec, command string) error {
	current, err := readCrontab()
	if err != nil {
		// Refusing leaves the schedule as it was, which is recoverable.
		// Writing would replace every job on this machine with ours.
		return err
	}
	var kept []string
	for _, l := range current {
		if !strings.Contains(l, "# "+marker) {
			kept = append(kept, l)
		}
	}
	if spec != "" {
		kept = append(kept, fmt.Sprintf("%s %s # %s", spec, command, marker))
	}
	return writeCrontab(kept)
}

// RemoveCron deletes a marked cron job.
func RemoveCron(marker string) error {
	return SetCron(marker, "", "")
}

var intervalRe = regexp.MustCompile(`^0 \*/(\d+) \* \* \*`)

// dailyIntervalRe matches the form HourlySpec writes for anything over a day.
//
// It was not matched by anything, so every interval above 24 hours read back as
// "disabled". The job itself was installed and ran exactly as asked — cron had
// it, the tunnels were refreshed — while the menu, the Telegram bot and the web
// panel all agreed it was off. An operator who set Auto Refresh to 48 hours saw
// it turned off the moment they looked again, set it once more, and ended up
// with the same job written twice over.
var dailyIntervalRe = regexp.MustCompile(`^0 0 \*/(\d+) \* \*`)

// GetIntervalHours returns the configured interval (in hours) for a marker, or
// 0 if it is not scheduled.
func GetIntervalHours(marker string) int {
	// A crontab that cannot be read is reported as "not scheduled", which is
	// what it has always said and is the only answer a function returning one
	// int can give. Nothing is written on this path, so the failure above
	// cannot happen here.
	current, _ := readCrontab()
	for _, l := range current {
		if !strings.Contains(l, "# "+marker) {
			continue
		}
		return intervalFromLine(l)
	}
	return 0
}

// intervalFromLine reads the interval out of one crontab line, or 0 for a line
// that is not one of the forms HourlySpec writes.
//
// Split out of GetIntervalHours so that what is written and what is read back
// can be checked against each other without a crontab. They disagreed for every
// interval over a day, and nothing short of installing one would have shown it.
func intervalFromLine(l string) int {
	switch {
	case strings.HasPrefix(l, "0 * * * *"):
		return 1
	case strings.HasPrefix(l, "0 0 * * *"):
		return 24
	}
	if m := intervalRe.FindStringSubmatch(l); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	// Tried after the hourly form, because "0 0 */2 * *" would otherwise have to
	// be told apart from "0 */2 * * *" by position alone.
	if m := dailyIntervalRe.FindStringSubmatch(l); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n * 24
	}
	return 0
}

// SetAutoRefresh schedules `backpack --restart-all` every `hours` hours.
// hours == 0 disables it.
func SetAutoRefresh(hours int) error {
	return SetCron(app.AutoRefreshMarker, HourlySpec(hours), app.BinPath+" --restart-all")
}

// AutoRefreshHours returns the current auto-refresh interval (0 = disabled).
func AutoRefreshHours() int {
	return GetIntervalHours(app.AutoRefreshMarker)
}
