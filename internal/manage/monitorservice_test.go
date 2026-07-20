package manage

import (
	"os"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/app"
)

// TestMonitorUnitContents checks the generated unit, because a unit file that
// is subtly wrong fails at boot on a remote machine where nobody is watching.
func TestMonitorUnitContents(t *testing.T) {
	unit := strings.ReplaceAll(monitorUnit, "%s", app.BinPath)

	// The flag must match what main.go actually parses; a typo here produces a
	// service that starts, opens the interactive menu, and hangs forever.
	if !strings.Contains(unit, app.BinPath+" --monitor") {
		t.Errorf("ExecStart does not invoke the monitor mode:\n%s", unit)
	}
	// It must come back on its own — a monitor that dies once and stays dead is
	// worse than none, because it is still trusted.
	if !strings.Contains(unit, "Restart=always") {
		t.Errorf("the unit does not restart automatically:\n%s", unit)
	}
	if !strings.Contains(unit, "WantedBy=multi-user.target") {
		t.Errorf("the unit would not start at boot:\n%s", unit)
	}
	// It must not depend on the panel: that coupling is the thing being removed.
	if strings.Contains(unit, app.WebUIService) {
		t.Errorf("the monitor unit depends on the web panel:\n%s", unit)
	}
}

// TestMonitorFlagIsParsed guards the link between the unit and the binary. The
// unit passes --monitor; main.go has to define that exact flag.
func TestMonitorFlagIsParsed(t *testing.T) {
	src, err := os.ReadFile("../../main.go")
	if err != nil {
		t.Skipf("cannot read main.go: %v", err)
	}
	if !strings.Contains(string(src), `flag.Bool("monitor"`) {
		t.Error(`main.go does not define a "monitor" flag, so the service would fall through to the interactive menu`)
	}
}

// TestMonitorServiceNameIsDistinct catches a copy-paste of the panel's name,
// which would make the two units overwrite each other.
func TestMonitorServiceNameIsDistinct(t *testing.T) {
	if app.MonitorService == app.WebUIService {
		t.Fatal("the monitor and the panel share a unit name")
	}
	for _, name := range []string{app.MonitorService, app.WebUIService} {
		if !strings.HasSuffix(name, ".service") {
			t.Errorf("%q is not a systemd unit name", name)
		}
	}
}

// TestUpdateRestartsTheMonitor guards a bug that is invisible from the outside.
//
// The unit text does not change between versions — only the binary it points at
// does. So EnsureMonitorService correctly decides there is nothing to do, and
// `systemctl start` is a no-op on a running service. The result is an update
// that appears to succeed while the monitor goes on running the previous
// version's code until the machine reboots. The update path must restart it.
func TestUpdateRestartsTheMonitor(t *testing.T) {
	src, err := os.ReadFile("update.go")
	if err != nil {
		t.Skipf("cannot read update.go: %v", err)
	}
	if !strings.Contains(string(src), "RestartMonitorService()") {
		t.Error("ApplyUpdate does not restart the monitor, so it would keep running the old binary")
	}
}

// A rollback replaces the binary too, so the same reasoning applies there.
func TestRollbackRestartsTheMonitor(t *testing.T) {
	src, err := os.ReadFile("snapshot.go")
	if err != nil {
		t.Skipf("cannot read snapshot.go: %v", err)
	}
	if !strings.Contains(string(src), "RestartMonitorService()") {
		t.Error("a rollback does not restart the monitor, leaving it on the version that failed")
	}
}

// The post-update health check has to judge the monitor as well. Without this,
// a new version whose monitor crashes on startup is scored healthy and kept,
// and the machine silently loses its watchdog and alerts.
func TestUpdateHealthCheckCoversTheMonitor(t *testing.T) {
	src, err := os.ReadFile("update.go")
	if err != nil {
		t.Skipf("cannot read update.go: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func unhealthyAfterUpdate")
	if i < 0 {
		t.Fatal("unhealthyAfterUpdate not found — this test needs updating")
	}
	if !strings.Contains(body[i:], "MonitorService") {
		t.Error("the post-update health check ignores the monitor service")
	}
}

// TestPanelDoesNotRunMonitorJobs is the regression guard for the whole change:
// the panel must not start the watchdog, the bot or the alerts, or stopping the
// monitor service would not actually stop anything.
func TestPanelDoesNotRunMonitorJobs(t *testing.T) {
	src, err := os.ReadFile("../webui/server.go")
	if err != nil {
		t.Skipf("cannot read the panel source: %v", err)
	}
	body := string(src)
	for _, call := range []string{"RunWatchdog", "RunBot", "RunAlerts"} {
		if strings.Contains(body, call+"(") {
			t.Errorf("the web panel still starts %s — it belongs to the monitor service", call)
		}
	}
}

// A restore replaces every tunnel's configuration on disk. If the services are
// only started rather than restarted, the ones already running keep the
// configuration they were launched with — so the restore appears to succeed and
// changes nothing. They also carry on writing their old traffic totals over the
// restored ones.
func TestRestoreRestartsTunnels(t *testing.T) {
	src, err := os.ReadFile("backup.go")
	if err != nil {
		t.Skipf("cannot read backup.go: %v", err)
	}
	body := string(src)

	i := strings.Index(body, "func Restore(")
	if i < 0 {
		t.Fatal("Restore not found — this test needs updating")
	}
	if !strings.Contains(body[i:], "RestartService(") {
		t.Error("Restore only starts tunnels, so running ones would keep their old configuration")
	}
}
