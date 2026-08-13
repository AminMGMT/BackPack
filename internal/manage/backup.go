package manage

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/schedule"
)

// backupMetaName is a synthetic entry stored inside the archive (not written to
// disk on restore) that captures settings living outside ConfigDir — currently
// the auto-refresh interval, which is kept in the crontab.
const backupMetaName = ".backpack-backup.json"

// backupMeta is the sidecar metadata embedded in every backup archive.
type backupMeta struct {
	Version          string `json:"version"`
	Created          string `json:"created"`
	AutoRefreshHours int    `json:"auto_refresh_hours"`
}

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
func WriteBackup(w io.Writer) error { return writeBackupTree(w, app.ConfigDir) }

// writeBackupTree is WriteBackup against a named root, so that the archive
// itself can be exercised against a tree a test builds rather than against the
// one real installation on the machine.
func writeBackupTree(w io.Writer, root string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	if err := writeBackupEntries(tw, root); err != nil {
		tw.Close()
		gz.Close()
		return err
	}

	// Both of these were deferred, and a deferred Close reports to nobody.
	// They are not cleanup: closing the tar writer writes the archive's footer
	// and closing the gzip writer writes the compressed trailer, so a failure
	// here — a full disk, a browser that hung up — is the difference between a
	// backup and most of one. Reported as an error, the caller deletes the file
	// and says so; discarded, it returns success over a truncated archive that
	// is not found to be short until the day it is needed.
	if err := tw.Close(); err != nil {
		gz.Close()
		return fmt.Errorf("finishing the archive: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("finishing compression: %w", err)
	}
	return nil
}

// writeBackupEntries puts the sidecar and the whole config tree into tw.
func writeBackupEntries(tw *tar.Writer, root string) error {
	// Sidecar metadata for settings that don't live under ConfigDir.
	meta := backupMeta{
		Version:          app.Version,
		Created:          time.Now().UTC().Format(time.RFC3339),
		AutoRefreshHours: schedule.AutoRefreshHours(),
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("describing the backup: %w", err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: backupMetaName,
		Mode: 0600,
		Size: int64(len(metaJSON)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(metaJSON); err != nil {
		return err
	}

	// Walk the config directory and add every file, preserving relative paths
	// and permissions (so 0600 key/config files stay 0600 on restore).
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		// Anything that is not a directory or an ordinary file is refused
		// rather than archived. A symlink here produced an entry with no
		// target recorded and no content behind it — neither the link nor the
		// file it pointed at — so the archive looked complete and restored to
		// something that was not. This directory is written by Backpack and
		// holds configs, certificates and JSON; a symlink in it is a surprise,
		// and the time to hear about a surprise in a backup is while it is
		// being taken.
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to back up %s: not a regular file (%s)", rel, info.Mode().Type())
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
			return tw.WriteHeader(hdr)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		return copyFileInto(tw, path)
	})
}

// copyFileInto streams one file into the archive and closes it before
// returning. The close used to be deferred inside the walk callback, which
// defers to the end of the walk rather than the end of the file: every file in
// the tree stayed open until the whole archive was written, so a config
// directory with enough in it ran the process out of descriptors.
func copyFileInto(tw *tar.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

// backupRetention is how many backup archives are kept in the backup folder;
// older ones are pruned automatically so backups never fill the disk.
const backupRetention = 10

// partialAge is how old an unfinished archive must be before it is swept up.
// Long enough that a backup running right now in another process is never
// mistaken for wreckage.
const partialAge = time.Hour

// pruneBackups deletes all but the newest backupRetention archives in dir, and
// sweeps up any partial file left behind by a backup that did not finish.
func pruneBackups(dir string) {
	sweepPartials(dir)

	matches, _ := filepath.Glob(filepath.Join(dir, "backpack-backup-*.tar.gz"))
	if len(matches) <= backupRetention {
		return
	}
	// Names are timestamped, so lexical order is chronological.
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	for _, old := range matches[backupRetention:] {
		os.Remove(old)
	}
}

// sweepPartials removes stale temporary archives. A backup that is interrupted
// hard enough — the machine loses power mid-write — never runs its own
// cleanup, and without this the leftovers accumulate in the backup directory
// for as long as the host lives. They are named so they cannot be mistaken for
// a backup and are not counted against the retention limit.
func sweepPartials(dir string) {
	matches, _ := filepath.Glob(filepath.Join(dir, ".backpack-backup-*.partial"))
	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil || time.Since(info.ModTime()) < partialAge {
			continue
		}
		os.Remove(path)
	}
}

// BackupToFile writes a timestamped backup archive into dir and returns its
// path. dir is created if missing, and old archives beyond the retention limit
// are pruned.
// It is written to a temporary file in the same directory and renamed into
// place once it is complete and on disk, so a name matching
// backpack-backup-*.tar.gz is always a whole archive. Writing straight to the
// final name meant an interrupted backup — a full disk, a reboot, a killed
// process — left a truncated file sitting under a name that says otherwise,
// which pruning then counts as one of the ten kept and a restore accepts as
// far as the point it was cut off.
func BackupToFile(dir string) (string, error) {
	return publishBackup(dir, WriteBackup)
}

// publishBackup writes whatever write produces into dir under a backup name,
// atomically. Taking the writer as an argument keeps the publishing — the
// temporary file, the fsync, the rename, the cleanup on failure — testable
// without a config directory to archive.
func publishBackup(dir string, write func(io.Writer) error) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	tmp, err := os.CreateTemp(dir, ".backpack-backup-*.partial")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	// Until the rename, every path out of here removes the partial file.
	defer func() {
		if tmpPath != "" {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	// CreateTemp makes it 0600 already; this says so out loud, because the
	// archive holds every tunnel token and private key on the host.
	if err := tmp.Chmod(0600); err != nil {
		return "", err
	}
	if err := write(tmp); err != nil {
		return "", err
	}
	// Fsync before the rename: without it the rename can reach the disk before
	// the bytes do, and a crash in between leaves a correctly named archive
	// with nothing, or part of something, inside it.
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	path, err := freeBackupPath(dir)
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", err
	}
	tmpPath = "" // published; the cleanup above must not delete it now

	syncDir(dir)
	pruneBackups(dir)
	return path, nil
}

// freeBackupPath returns a timestamped archive path that nothing is using.
//
// The timestamp resolves to the second, so an automatic backup and one taken
// from the panel or the bot in that same second used to land on one name and
// the second quietly replaced the first. A suffix is appended rather than a
// random name so that the lexical order the pruning relies on is still
// chronological.
func freeBackupPath(dir string) (string, error) {
	stamp := time.Now().Format("20060102-150405")
	for n := 0; n < 100; n++ {
		name := fmt.Sprintf("backpack-backup-%s.tar.gz", stamp)
		if n > 0 {
			name = fmt.Sprintf("backpack-backup-%s-%d.tar.gz", stamp, n)
		}
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path, nil
		}
	}
	return "", fmt.Errorf("could not find an unused backup name in %s", dir)
}

// syncDir flushes a directory entry so a rename survives a crash. Best effort:
// the backup is already written and named, and a platform that will not open a
// directory is not a reason to report failure.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
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
