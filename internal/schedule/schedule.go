// Package schedule manages recurring backpack jobs via the system crontab
// (auto-refresh of tunnels and periodic Telegram reports).
package schedule

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/backpack/backpack/internal/app"
)

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

// readCrontab returns the current crontab lines (empty if none).
func readCrontab() []string {
	out, err := exec.Command("crontab", "-l").Output()
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
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
	var kept []string
	for _, l := range readCrontab() {
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

var intervalRe = regexp.MustCompile(`0 \*/(\d+) \* \* \*`)

// GetIntervalHours returns the configured interval (in hours) for a marker, or
// 0 if it is not scheduled.
func GetIntervalHours(marker string) int {
	for _, l := range readCrontab() {
		if !strings.Contains(l, "# "+marker) {
			continue
		}
		if strings.HasPrefix(l, "0 * * * *") {
			return 1
		}
		if strings.HasPrefix(l, "0 0 * * *") {
			return 24
		}
		if m := intervalRe.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
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
