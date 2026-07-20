package manage

import (
	"fmt"
	"os"

	"github.com/backpack/backpack/internal/app"
)

// The monitor service.
//
// Watching the tunnels, running the Telegram bot and sending alerts used to
// happen inside the web-panel process. That made the panel a dependency of
// monitoring, which is backwards: stopping the panel — or the panel crashing,
// or the user turning it off because they only wanted the CLI — silently
// stopped the watchdog restarting dropped tunnels and stopped every alert. The
// failure was invisible, which is the worst property a monitor can have.
//
// It now runs as its own systemd unit that depends on nothing but the machine
// being up. This file only manages the unit; the work itself lives in
// internal/monitor, which imports this package.

// monitorUnit is the systemd unit for the monitor service.
const monitorUnit = `[Unit]
Description=Backpack Monitor (watchdog, Telegram bot and alerts)
After=network.target

[Service]
Type=simple
ExecStart=%s --monitor
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`

// EnsureMonitorService installs and starts the monitor unit. It is idempotent,
// so it is safe to call on every menu launch and after every update — which is
// also how an install that predates the service acquires it.
func EnsureMonitorService() error {
	path := app.ServiceDir + "/" + app.MonitorService
	want := fmt.Sprintf(monitorUnit, app.BinPath)

	// Only touch systemd when the unit actually changed, so a normal launch
	// does not churn the daemon or bounce a healthy monitor.
	current, err := os.ReadFile(path)
	unchanged := err == nil && string(current) == want

	if unchanged {
		if IsActive(app.MonitorService) {
			return nil
		}
		return StartService(app.MonitorService)
	}

	if err := os.WriteFile(path, []byte(want), 0644); err != nil {
		return err
	}
	if err := DaemonReload(); err != nil {
		return err
	}
	// A rewritten unit has to be restarted, not started: `systemctl start` is a
	// no-op on a service that is already active, which would leave the old
	// definition running while the file on disk says something else.
	if IsActive(app.MonitorService) {
		return RestartService(app.MonitorService)
	}
	return StartService(app.MonitorService)
}

// RestartMonitorService installs the unit if needed and always restarts the
// process.
//
// Used after an update. EnsureMonitorService alone is not enough there: the
// unit text does not change when only the binary is replaced, so it would
// correctly decide there is nothing to do — and the monitor would go on running
// the previous version's code until the machine rebooted.
func RestartMonitorService() error {
	if err := EnsureMonitorService(); err != nil {
		return err
	}
	return RestartService(app.MonitorService)
}

// DisableMonitorService stops and removes the monitor unit.
func DisableMonitorService() error {
	if IsActive(app.MonitorService) || IsEnabled(app.MonitorService) {
		DisableService(app.MonitorService)
	}
	os.Remove(app.ServiceDir + "/" + app.MonitorService)
	return DaemonReload()
}

// MonitorRunning reports whether the monitor service is active.
func MonitorRunning() bool { return IsActive(app.MonitorService) }
