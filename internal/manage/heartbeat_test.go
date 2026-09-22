package manage

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Noticing that the watchdog has gone quiet.
//
// The distinction this has to keep is between "not running" and "cannot tell",
// because reporting the second as the first is how a health check stops being
// read. A machine that never had the monitor, and a machine in the first
// minutes after an update, must both come back as "nothing to say".

func isolateHeartbeat(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := heartbeatPath
	heartbeatPath = filepath.Join(dir, "monitor-heartbeat")
	t.Cleanup(func() { heartbeatPath = old })
	return dir
}

func TestAHeartbeatRoundTrips(t *testing.T) {
	isolateHeartbeat(t)
	RecordMonitorHeartbeat()

	at, ok := MonitorHeartbeat()
	if !ok {
		t.Fatal("a heartbeat that was just written could not be read")
	}
	if d := time.Since(at); d > time.Minute || d < -time.Second {
		t.Fatalf("the heartbeat is %v old, want about now", d)
	}
}

// No file, an unreadable one, or one from a version that predates this are all
// "cannot tell" — never "the monitor is down".
func TestAnAbsentOrUnreadableHeartbeatIsNotAFailure(t *testing.T) {
	dir := isolateHeartbeat(t)

	if _, ok := MonitorHeartbeat(); ok {
		t.Error("a heartbeat was read from nothing")
	}

	for _, junk := range []string{"", "not a number", "-1", "0"} {
		if err := os.WriteFile(heartbeatPath, []byte(junk), 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if _, ok := MonitorHeartbeat(); ok {
			t.Errorf("%q was accepted as a heartbeat", junk)
		}
	}
	_ = dir
}

// The stale reading is the whole point: a service systemd is happy with, and a
// watchdog that has not run a pass.
func TestAStaleHeartbeatIsRecognised(t *testing.T) {
	isolateHeartbeat(t)

	old := time.Now().Add(-heartbeatStale - time.Minute)
	if err := os.WriteFile(heartbeatPath,
		[]byte(strconv.FormatInt(old.Unix(), 10)), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	at, ok := MonitorHeartbeat()
	if !ok {
		t.Fatal("a stale heartbeat could not be read")
	}
	if time.Since(at) <= heartbeatStale {
		t.Fatalf("the heartbeat reads as %v old, which is not stale", time.Since(at))
	}
}

// A machine that never installed the monitor has not had its watchdog fail.
// MonitorSilent has to answer false there, whatever the heartbeat file says.
func TestAMachineWithNoMonitorIsNotReportedAsSilent(t *testing.T) {
	isolateHeartbeat(t)
	// No unit is installed on the machine running this test, so this exercises
	// the first guard.
	if silent, _ := MonitorSilent(); silent {
		t.Error("a machine with no monitor unit was reported as having a silent watchdog")
	}
}
