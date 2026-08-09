package manage

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/schedule"
)

// backupMeta is the sidecar metadata embedded in every backup archive.
type backupMeta = archiveMetadata

// RestoreResult summarises what a restore put back in place.
type RestoreResult struct {
	Files            int      // config files written to disk
	Tunnels          []string // tunnels re-registered as systemd services
	Started          int      // tunnels successfully started
	Failed           int      // tunnels that failed to start
	WebUIConfig      bool     // webui.json was present in the archive
	TelegramConfig   bool     // telegram.json was present in the archive
	AutoRefreshHours int      // auto-refresh interval restored from the archive
}

// WriteBackup streams a gzip-compressed tar of the entire config directory.
//
// Exported because the web panel streams a backup straight to the browser as a
// download; everything else goes through BackupToFile.
// (every tunnel TOML, webui.json, telegram.json, certificates, meta and the
// recorded install path) plus a small sidecar capturing the auto-refresh
// schedule, to w. It is the single source for both the CLI and web downloads.
func WriteBackup(w io.Writer) error {
	return writeBackupArchive(w, app.ConfigDir, currentBackupMeta())
}

func currentBackupMeta() backupMeta {
	return backupMeta{
		Version:          app.Version,
		Created:          time.Now().UTC().Format(time.RFC3339),
		AutoRefreshHours: schedule.AutoRefreshHours(),
	}
}

// backupRetention is how many backup archives are kept in the backup folder;
// older ones are pruned automatically so backups never fill the disk.
const backupRetention = 10

// BackupToFile writes a timestamped backup archive into dir and returns its
// path. dir is created if missing, and old archives beyond the retention limit
// are pruned.
func BackupToFile(dir string) (string, error) {
	return publishBackupFile(dir, app.ConfigDir, currentBackupMeta(), backupRetention)
}

// Restore reads a backup archive produced by WriteBackup, extracts it into the
// config directory (overwriting matching files, leaving others untouched), then
// re-registers a systemd service for every tunnel it finds and starts them. It
// also restores the auto-refresh schedule from the archive sidecar.
//
// The caller is responsible for (re)starting the web-panel service afterwards —
// that lives in the webui package to avoid an import cycle.
func Restore(r io.Reader) (RestoreResult, error) {
	var res RestoreResult

	gz, err := gzip.NewReader(r)
	if err != nil {
		return res, fmt.Errorf("not a valid backup archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		return res, err
	}

	sawConfig := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, fmt.Errorf("reading archive: %w", err)
		}

		// Sidecar: parse for out-of-tree settings, never write it to disk.
		if hdr.Name == backupMetaName {
			var m backupMeta
			if data, err := io.ReadAll(tr); err == nil {
				_ = json.Unmarshal(data, &m)
				res.AutoRefreshHours = m.AutoRefreshHours
			}
			continue
		}

		// Guard against path traversal (zip-slip): the cleaned target must stay
		// inside ConfigDir.
		clean := filepath.Clean(hdr.Name)
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return res, fmt.Errorf("refusing unsafe path in archive: %q", hdr.Name)
		}

		// install_path is machine-specific — it records where install.sh cloned
		// the repo on THIS host. Keep the local value if present; only fall back
		// to the archived one when none exists, so restoring on a new server
		// doesn't point the updater at a directory that isn't there.
		if clean == filepath.Base(app.InstallPathFile) && fileExists(app.InstallPathFile) {
			continue
		}
		target := filepath.Join(app.ConfigDir, clean)
		if rel, err := filepath.Rel(app.ConfigDir, target); err != nil || strings.HasPrefix(rel, "..") {
			return res, fmt.Errorf("refusing unsafe path in archive: %q", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return res, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return res, err
			}
			mode := os.FileMode(hdr.Mode).Perm()
			if mode == 0 {
				mode = 0600
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return res, err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return res, err
			}
			f.Close()
			res.Files++

			base := filepath.Base(target)
			switch {
			case base == filepath.Base(app.WebUIConfig):
				res.WebUIConfig = true
			case base == filepath.Base(app.TelegramConfig):
				res.TelegramConfig = true
			case strings.HasSuffix(base, ".toml"):
				sawConfig = true
			}
		}
	}

	if sawConfig {
		// Re-register a systemd unit for every restored tunnel, then start them.
		tunnels := List()
		unitFailed := map[string]bool{}
		for _, t := range tunnels {
			res.Tunnels = append(res.Tunnels, t.Name)
			if err := writeUnit(t.Name); err != nil {
				unitFailed[t.Name] = true
			}
		}
		_ = DaemonReload()
		for _, t := range tunnels {
			if unitFailed[t.Name] {
				res.Failed++
				continue
			}
			// Enabled first so it survives a reboot, then restarted.
			//
			// Starting is not enough: `systemctl start` does nothing to a
			// service that is already running, so a tunnel that was up would
			// carry on with the configuration it was started with and quietly
			// ignore the one just restored. It would also keep writing its old
			// traffic totals over the restored ones.
			if err := StartService(app.ServiceName(t.Name)); err != nil {
				res.Failed++
				continue
			}
			if err := RestartService(app.ServiceName(t.Name)); err != nil {
				res.Failed++
				continue
			}
			res.Started++
		}
	}

	// Restore the auto-refresh schedule captured in the sidecar.
	if res.AutoRefreshHours > 0 {
		_ = schedule.SetAutoRefresh(res.AutoRefreshHours)
	}

	return res, nil
}
