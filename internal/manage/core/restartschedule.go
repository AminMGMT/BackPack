package core

import (
	"fmt"
	"os"
	"regexp"
	"strconv"

	"github.com/backpack/backpack/internal/app"
)

// A tunnel restarted on a schedule, at the same moment on both of its servers.
//
// Auto Refresh restarts every tunnel on a machine from cron, and cron runs in
// the machine's own timezone — an Iran server on +03:30 and a kharej on UTC
// given "every 6 hours" restart three and a half hours apart, so each restart
// is two outages. This is per tunnel, and it is a systemd timer written in UTC,
// so the two ends of one tunnel, told the same hours and minute, restart
// together wherever they are. The Iran side picks the numbers and the setup
// link carries them to the kharej.

// scheduledRestartUnit is the name of the timer, and of the one-shot service it
// starts, for a tunnel. Not "backpack-<name>…": that is the tunnel's own unit
// pattern, and a second unit matching it would be read as a second tunnel by
// anything that looks at unit names.
func scheduledRestartUnit(name, kind string) string {
	return "backpack-restart-" + name + "." + kind
}

// restartCalendar is the OnCalendar expression for every hours hours at minute
// past the hour, in UTC. Below a day the hours are counted from midnight UTC,
// as cron counts them; a day or more is every so many days at 00:minute.
func restartCalendar(hours, minute int) string {
	switch {
	case hours < 24:
		return fmt.Sprintf("*-*-* 00/%d:%02d:00 UTC", hours, minute)
	case hours == 24:
		return fmt.Sprintf("*-*-* 00:%02d:00 UTC", minute)
	default:
		return fmt.Sprintf("*-*-01/%d 00:%02d:00 UTC", hours/24, minute)
	}
}

// SetScheduledRestart makes a tunnel restart every hours hours at minute past
// the hour, UTC. hours 0 removes the schedule.
func SetScheduledRestart(name string, hours, minute int) error {
	if err := CheckName(name); err != nil {
		return err
	}
	timer := scheduledRestartUnit(name, "timer")
	service := scheduledRestartUnit(name, "service")
	if hours <= 0 {
		removeScheduledRestart(name)
		return nil
	}
	hours = EffectiveRestartHours(hours)
	minute = ((minute % 60) + 60) % 60
	svc := fmt.Sprintf(`[Unit]
Description=Scheduled restart of Backpack tunnel %s

[Service]
Type=oneshot
ExecStart=/bin/systemctl try-restart %s
`, name, app.ServiceName(name))
	tmr := fmt.Sprintf(`[Unit]
Description=Restart Backpack tunnel %s every %d hours (UTC, both servers at once)

[Timer]
OnCalendar=%s
AccuracySec=1s

[Install]
WantedBy=timers.target
`, name, hours, restartCalendar(hours, minute))
	if err := os.WriteFile(app.ServiceDir+"/"+service, []byte(svc), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(app.ServiceDir+"/"+timer, []byte(tmr), 0644); err != nil {
		return err
	}
	if err := DaemonReload(); err != nil {
		return err
	}
	_, err := Systemctl("enable", "--now", timer)
	return err
}

// removeScheduledRestart stops and deletes a tunnel's schedule, if it has one.
func removeScheduledRestart(name string) {
	timer := scheduledRestartUnit(name, "timer")
	path := app.ServiceDir + "/" + timer
	if _, err := os.Stat(path); err != nil {
		return
	}
	_, _ = Systemctl("disable", "--now", timer)
	os.Remove(path)
	os.Remove(app.ServiceDir + "/" + scheduledRestartUnit(name, "service"))
	_ = DaemonReload()
}

// EffectiveRestartHours is the interval a schedule really gets: any number of
// hours below a day, whole days above it.
func EffectiveRestartHours(hours int) int {
	switch {
	case hours <= 0:
		return 0
	case hours <= 24:
		return hours
	default:
		return (hours / 24) * 24
	}
}

var restartCalendarRe = regexp.MustCompile(`(?m)^OnCalendar=\*-\*-(\*|01/(\d+)) (?:00/(\d+)|00):(\d{2}):00 UTC$`)

// ScheduledRestart reports a tunnel's schedule: every hours hours at minute
// past, UTC. hours is 0 when it has none.
func ScheduledRestart(name string) (hours, minute int) {
	b, err := os.ReadFile(app.ServiceDir + "/" + scheduledRestartUnit(name, "timer"))
	if err != nil {
		return 0, 0
	}
	m := restartCalendarRe.FindSubmatch(b)
	if m == nil {
		return 0, 0
	}
	minute, _ = strconv.Atoi(string(m[4]))
	switch {
	case len(m[2]) > 0:
		days, _ := strconv.Atoi(string(m[2]))
		return days * 24, minute
	case len(m[3]) > 0:
		h, _ := strconv.Atoi(string(m[3]))
		return h, minute
	default:
		return 24, minute
	}
}
