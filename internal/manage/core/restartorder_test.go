package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/app"
)

func recordSystemctl(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	old := runSystemctl
	runSystemctl = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", nil
	}
	oldRestart, oldUpdate := restartMonitor, updateMonitorUnit
	restartMonitor = func() error { calls = append(calls, "restart "+app.MonitorService); return nil }
	updateMonitorUnit = func() error { return nil }
	t.Cleanup(func() { runSystemctl, restartMonitor, updateMonitorUnit = old, oldRestart, oldUpdate })
	return &calls
}

func fakeCgroup(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cgroup")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := procSelfCgroup
	procSelfCgroup = path
	t.Cleanup(func() { procSelfCgroup = old })
}

func TestOwnUnitReadsTheCgroup(t *testing.T) {
	for body, want := range map[string]string{
		"0::/system.slice/backpack-webui.service\n":                                app.WebUIService,
		"12:pids:/\n1:name=systemd:/system.slice/backpack-monitor.service\n0::/\n": app.MonitorService,
		"0::/user.slice/user-0.slice/session-3.scope\n":                            "",
	} {
		fakeCgroup(t, body)
		if got := OwnUnit(); got != want {
			t.Errorf("cgroup %q: OwnUnit() = %q, want %q", body, got, want)
		}
	}
}

// Started from the panel, the update must not restart the panel's own unit in
// line — that stops the process doing the update before it reaches the tunnels.
func TestTheCallersOwnUnitIsLeftForLast(t *testing.T) {
	calls := recordSystemctl(t)
	fakeCgroup(t, "0::/system.slice/"+app.WebUIService+"\n")

	_, _, later := RestartForNewBinary(nil)

	for _, c := range *calls {
		if c == "restart "+app.WebUIService {
			t.Fatalf("the panel restarted its own unit in line: %v", *calls)
		}
	}
	if len(later) != 1 || later[0] != app.WebUIService {
		t.Fatalf("later = %v, want the panel's unit", later)
	}

	// Nothing happens until the caller has reported and asks for it.
	before := len(*calls)
	FinishDeferredRestarts()
	if len(*calls) != before+1 {
		t.Fatalf("FinishDeferredRestarts made %d calls, want one", len(*calls)-before)
	}
	FinishDeferredRestarts() // taken once; a second call has nothing left
	if len(*calls) != before+1 {
		t.Fatal("the deferred restart ran twice")
	}
	if last := (*calls)[before]; last != "--no-block restart "+app.WebUIService {
		t.Fatalf("the deferred restart was %q, want a non-blocking restart", last)
	}
}

// From an SSH session nothing hosts the caller, so everything restarts in line.
func TestFromTheMonitorTheMonitorIsLeftForLast(t *testing.T) {
	calls := recordSystemctl(t)
	t.Cleanup(func() { deferred.take() })
	fakeCgroup(t, "0::/system.slice/"+app.MonitorService+"\n")

	_, _, later := RestartForNewBinary(nil)
	for _, c := range *calls {
		if c == "restart "+app.MonitorService {
			t.Fatalf("the bot restarted its own unit in line: %v", *calls)
		}
	}
	if len(later) != 1 || later[0] != app.MonitorService {
		t.Fatalf("later = %v, want the monitor's unit", later)
	}
}

func TestFromASessionEverythingRestartsInLine(t *testing.T) {
	calls := recordSystemctl(t)
	fakeCgroup(t, "0::/user.slice/user-0.slice/session-3.scope\n")

	_, _, later := RestartForNewBinary(nil)
	if len(later) != 0 {
		t.Fatalf("later = %v, want nothing deferred", later)
	}
	joined := strings.Join(*calls, "\n")
	for _, unit := range []string{app.MonitorService, app.WebUIService} {
		if !strings.Contains(joined, "restart "+unit) {
			t.Fatalf("%s was not restarted: %v", unit, *calls)
		}
	}
}
