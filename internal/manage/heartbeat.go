package manage

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// Something that notices when the watchdog is not running.
//
// The monitor service is what watches the tunnels, and nothing watched it. Its
// unit says Restart=always, so a crash brings it back — but a crash *loop*, a
// service somebody stopped to debug something and left stopped, or a unit that
// never got installed on an older machine all produce the same silence. And
// silence from a watchdog is indistinguishable from a fleet with nothing wrong,
// which is the worst property a monitoring component can have.
//
// The fix is a heartbeat rather than a second watcher: a second watcher needs a
// third. The monitor writes a timestamp as it goes round; anything that runs
// independently of it — Diagnose, the panel's health screen, the CLI — reads
// that file and can say how long ago the watchdog last ran. Nothing new has to
// be kept alive for this to work.

// heartbeatPath is where the monitor records that it is running. A variable so
// a test can point it somewhere harmless.
var heartbeatPath = filepath.Join(app.ConfigDir, "monitor-heartbeat")

// heartbeatStale is how far behind the file may fall before it means something.
//
// The monitor writes on the watchdog's own cadence, so a gap of one or two
// intervals is ordinary — a slow pass, a machine under load. Three minutes is
// several intervals and is not.
const heartbeatStale = 3 * time.Minute

// RecordMonitorHeartbeat says the monitor is alive now. Called from its loop.
//
// Failures are ignored on purpose: a monitor that cannot write a heartbeat
// should carry on watching tunnels, and the consequence of the failure — this
// looking stale — is already the thing being reported.
func RecordMonitorHeartbeat() {
	_ = os.WriteFile(heartbeatPath, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o644)
}

// MonitorHeartbeat returns when the monitor last said it was running.
//
// ok is false when there is nothing to go on: no file, an unreadable one, or
// one written by a version that predates this. That is not the same as "the
// monitor is down" and callers must not report it as such — on the machines
// that most need this check, it is simply the first few minutes after an
// update.
func MonitorHeartbeat() (at time.Time, ok bool) {
	b, err := os.ReadFile(heartbeatPath)
	if err != nil {
		return time.Time{}, false
	}
	secs, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil || secs <= 0 {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}

// MonitorSilent reports whether the watchdog has stopped saying anything, and
// for how long.
//
// It answers false while the unit is not installed or not enabled: a machine
// that has never had the monitor is not a machine whose monitor has failed, and
// telling an operator their watchdog is down when they never asked for one is
// how a health check stops being read.
func MonitorSilent() (silent bool, since time.Duration) {
	if !fileExists(app.ServiceDir + "/" + app.MonitorService) {
		return false, 0
	}
	if !IsEnabled(app.MonitorService) && !IsActive(app.MonitorService) {
		return false, 0
	}
	at, ok := MonitorHeartbeat()
	if !ok {
		return false, 0
	}
	if gap := time.Since(at); gap > heartbeatStale {
		return true, gap
	}
	return false, 0
}
