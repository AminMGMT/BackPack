package manage

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/backpack/backpack/internal/alerthist"
	"github.com/backpack/backpack/internal/app"
)

// Weekly automatic backups.
//
// A backup that exists only when somebody remembered to take one is a backup
// taken the day before it was needed, usually not. With this on, the monitor
// service writes one archive a week into the standard backups folder, pruned
// by the same retention as manual ones.

const autoBackupEvery = 7 * 24 * time.Hour

// autoBackupFlag marks the choice on disk, next to the tunnel configs so a
// backup carries it along.
var autoBackupFlag = app.ConfigDir + "/autobackup"

// AutoBackupEnabled reports whether weekly backups are switched on.
func AutoBackupEnabled() bool {
	_, err := os.Stat(autoBackupFlag)
	return err == nil
}

// SetAutoBackup switches weekly backups on or off.
func SetAutoBackup(on bool) error {
	if !on {
		err := os.Remove(autoBackupFlag)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(autoBackupFlag, []byte("weekly\n"), 0644)
}

// newestBackupTime returns when the most recent archive was written, zero if
// there is none. Names are timestamped so lexical order is chronological, but
// the file's own mtime is what actually answers "how old".
func newestBackupTime() time.Time {
	matches, _ := filepath.Glob(filepath.Join(app.BackupDir, "backpack-backup-*.tar.gz"))
	if len(matches) == 0 {
		return time.Time{}
	}
	sort.Strings(matches)
	fi, err := os.Stat(matches[len(matches)-1])
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// RunAutoBackup takes a weekly backup until ctx is cancelled. It checks
// hourly rather than sleeping a week, so switching the setting on takes
// effect within the hour and a reboot cannot skip a due backup.
func RunAutoBackup(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		autoBackupPass()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func autoBackupPass() {
	if !AutoBackupEnabled() {
		return
	}
	if last := newestBackupTime(); !last.IsZero() && time.Since(last) < autoBackupEvery {
		return
	}
	path, err := BackupToFile(app.BackupDir)
	if err != nil {
		alerthist.RecordEvent("💾 Weekly auto-backup FAILED: " + err.Error())
		return
	}
	alerthist.RecordEvent("💾 Weekly auto-backup saved: " + filepath.Base(path))
}
