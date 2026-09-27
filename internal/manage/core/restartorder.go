package core

import (
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/backpack/backpack/internal/app"
)

// Restarting everything onto a new binary, from wherever the request came.
//
// An update can be started from the SSH menu, from the web panel and from the
// Telegram bot. The last two run inside units the update itself restarts — the
// panel in backpack-webui, the bot in backpack-monitor — and `systemctl
// restart` of the unit that contains the caller stops the caller: systemd sends
// every process of the unit SIGTERM and waits for it. The update used to
// restart the panel first, then the monitor, then the tunnels, then check them
// and roll back if they failed; started from the panel it died at the first
// step, and the tunnels, the health check and the rollback never happened.
//
// So the order is fixed here, once, for the update, the local update and the
// rollback alike: tunnels first, then the monitor and the panel — and whichever
// of those hosts the caller is not restarted here at all. It is recorded, and
// the caller restarts it with FinishDeferredRestarts once it has reported the
// outcome — the report is the last thing it can do, since the restart ends it.
//
// What that costs: a panel or monitor that fails to start on the new binary is
// not caught by the update's own health check, because it is started after it.
// systemd's Restart= keeps retrying it, and the SSH menu remains the way in.

// procSelfCgroup is where the kernel says which cgroup — and so which systemd
// unit — this process belongs to. A variable so a test can point it elsewhere.
var procSelfCgroup = "/proc/self/cgroup"

// OwnUnit names the systemd service unit this process runs in, or "" when it
// runs in none (an SSH session, a test).
func OwnUnit() string {
	b, err := os.ReadFile(procSelfCgroup)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		// "0::/system.slice/backpack-webui.service" on cgroup v2,
		// "1:name=systemd:/system.slice/…" on v1.
		i := strings.LastIndexByte(line, ':')
		if i < 0 {
			continue
		}
		for _, part := range strings.Split(line[i+1:], "/") {
			if strings.HasSuffix(part, ".service") {
				return part
			}
		}
	}
	return ""
}

// RestartForNewBinary restarts every tunnel, then the monitor and the panel, and
// returns the units it deliberately left for RestartLater because the caller
// runs inside them.
func RestartForNewBinary(logf func(string)) (ok, failed int, later []string) {
	if logf == nil {
		logf = func(string) {}
	}
	ok, failed = RestartAll()

	own := OwnUnit()
	if own == app.MonitorService {
		// Still bring the unit file up to date; only the restart waits.
		if err := updateMonitorUnit(); err != nil {
			logf("Warning: the monitor's unit could not be updated: " + err.Error())
		}
		later = append(later, app.MonitorService)
	} else if err := restartMonitor(); err != nil {
		logf("Warning: monitor service could not start: " + err.Error())
	}

	if own == app.WebUIService {
		later = append(later, app.WebUIService)
	} else {
		RestartService(app.WebUIService)
	}
	deferred.add(later)
	return ok, failed, later
}

// deferred holds the units a caller has still to restart; see
// FinishDeferredRestarts.
var deferred deferredUnits

type deferredUnits struct {
	mu    sync.Mutex
	units []string
}

func (d *deferredUnits) add(units []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, u := range units {
		if !slices.Contains(d.units, u) {
			d.units = append(d.units, u)
		}
	}
}

func (d *deferredUnits) take() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.units
	d.units = nil
	return out
}

// FinishDeferredRestarts restarts the units RestartForNewBinary left for last
// because the caller runs inside them. The caller calls it after it has
// reported how the update went; it returns, and the process ends shortly after.
func FinishDeferredRestarts() { RestartLater(deferred.take()) }

// RestartLater queues a restart of units without waiting for it. It is the last
// thing a caller running inside one of them does: systemd stops that caller as
// part of the restart, so nothing after this line can be relied on to run.
func RestartLater(units []string) {
	if len(units) == 0 {
		return
	}
	_, _ = Systemctl(append([]string{"--no-block", "restart"}, units...)...)
	unitCache.Forget()
}

// The two monitor steps, as variables so a test does not write a unit file into
// the real systemd directory.
var (
	restartMonitor    = RestartMonitorService
	updateMonitorUnit = writeMonitorUnit
)

// writeMonitorUnit brings the monitor's unit file up to date without touching
// the running process.
func writeMonitorUnit() error {
	_, err := writeMonitorUnitIfChanged()
	return err
}
