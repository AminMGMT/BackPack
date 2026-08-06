package manage

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/engine"
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
func WriteBackup(w io.Writer) error {
	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	// Sidecar metadata for settings that don't live under ConfigDir.
	meta := backupMeta{
		Version:          app.Version,
		Created:          time.Now().UTC().Format(time.RFC3339),
		AutoRefreshHours: schedule.AutoRefreshHours(),
	}
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
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
	root := app.ConfigDir
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
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
}

// backupRetention is how many backup archives are kept in the backup folder;
// older ones are pruned automatically so backups never fill the disk.
const backupRetention = 10

// pruneBackups deletes all but the newest backupRetention archives in dir.
func pruneBackups(dir string) {
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

// BackupToFile writes a timestamped backup archive into dir and returns its
// path. dir is created if missing, and old archives beyond the retention limit
// are pruned.
func BackupToFile(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("backpack-backup-%s.tar.gz", time.Now().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	if err := WriteBackup(f); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	pruneBackups(dir)
	return path, nil
}

func configsInDir(dir string) (map[string]*config.Config, error) {
	result := map[string]*config.Config{}
	paths, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		cfg, err := config.LoadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		result[strings.TrimSuffix(filepath.Base(path), ".toml")] = cfg
	}
	return result, nil
}

func validateRestoreSet(ctx context.Context, dir string, configs map[string]*config.Config) error {
	for name, cfg := range configs {
		provider, err := engine.Resolve(cfg)
		if err == nil {
			err = provider.Validate(ctx, engine.Request{ConfigPath: filepath.Join(dir, name+".toml"), Config: cfg})
		}
		if err != nil {
			return fmt.Errorf("restored instance %s is invalid or conflicts with this host: %w", name, err)
		}
	}
	return nil
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
	parent := filepath.Dir(app.ConfigDir)
	stage, err := os.MkdirTemp(parent, ".backpack-restore-")
	if err != nil {
		return res, err
	}
	stageLive := true
	defer func() {
		if stageLive {
			_ = os.RemoveAll(stage)
		}
	}()
	// Restore remains additive: begin with the current tree, then overlay the
	// archive in staging. No live file is touched before all candidates pass.
	if err := copyTree(app.ConfigDir, stage); err != nil {
		return res, fmt.Errorf("prepare restore candidate: %w", err)
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
		target := filepath.Join(stage, clean)
		if rel, err := filepath.Rel(stage, target); err != nil || strings.HasPrefix(rel, "..") {
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

	if !sawConfig {
		// Non-tunnel settings still use the same atomic directory swap below.
	}
	candidates, err := configsInDir(stage)
	if err != nil {
		return res, err
	}
	if err := validateRestoreSet(context.Background(), stage, candidates); err != nil {
		return res, err
	}

	oldTunnels := List()
	oldActive := map[string]bool{}
	oldNames := map[string]bool{}
	for _, t := range oldTunnels {
		oldNames[t.Name] = true
		oldActive[t.Name] = IsActive(t.Service)
		if oldActive[t.Name] {
			if err := StopService(t.Service); err != nil {
				return res, fmt.Errorf("quiesce %s before restore: %w", t.Name, err)
			}
		}
	}
	resumeOld := func() {
		for _, t := range oldTunnels {
			if oldActive[t.Name] {
				_ = StartService(t.Service)
			}
		}
	}

	rollbackDir, err := os.MkdirTemp(parent, ".backpack-rollback-")
	if err != nil {
		resumeOld()
		return res, err
	}
	_ = os.Remove(rollbackDir)
	if err = os.Rename(app.ConfigDir, rollbackDir); err != nil {
		resumeOld()
		return res, fmt.Errorf("create restore point: %w", err)
	}
	if err = os.Rename(stage, app.ConfigDir); err != nil {
		_ = os.Rename(rollbackDir, app.ConfigDir)
		resumeOld()
		return res, fmt.Errorf("activate restore candidate: %w", err)
	}
	stageLive = false

	rollback := func(cause error) error {
		for name := range candidates {
			if oldNames[name] {
				_ = StopService(app.ServiceName(name))
			} else {
				_ = DisableService(app.ServiceName(name))
				removeUnit(name)
			}
		}
		failedDir, _ := os.MkdirTemp(parent, ".backpack-failed-restore-")
		if failedDir != "" {
			_ = os.Remove(failedDir)
			_ = os.Rename(app.ConfigDir, failedDir)
		}
		if e := os.Rename(rollbackDir, app.ConfigDir); e != nil {
			return errors.Join(cause, fmt.Errorf("restore rollback failed: %w", e))
		}
		for _, t := range oldTunnels {
			_ = writeUnit(t.Name)
		}
		_ = DaemonReload()
		for _, t := range oldTunnels {
			if oldActive[t.Name] {
				_ = StartService(t.Service)
			}
		}
		if failedDir != "" {
			_ = os.RemoveAll(failedDir)
		}
		return cause
	}

	var names []string
	for name := range candidates {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		res.Tunnels = append(res.Tunnels, name)
		if err := writeUnit(name); err != nil {
			return res, rollback(fmt.Errorf("write restored unit %s: %w", name, err))
		}
	}
	if err := DaemonReload(); err != nil {
		return res, rollback(fmt.Errorf("reload restored units: %w", err))
	}
	for _, name := range names {
		service := app.ServiceName(name)
		if err := StartService(service); err != nil {
			res.Failed++
			return res, rollback(fmt.Errorf("start restored instance %s: %w", name, err))
		}
		if !WaitServiceActive(service, 12*time.Second) {
			res.Failed++
			return res, rollback(fmt.Errorf("restored instance %s did not become active", name))
		}
		if candidates[name].EffectiveEngine() == config.EngineIPTables {
			provider, _ := engine.Resolve(candidates[name])
			deadline := time.Now().Add(12 * time.Second)
			ready := false
			for time.Now().Before(deadline) {
				h, _ := provider.Health(context.Background(), engine.Request{ConfigPath: app.ConfigPath(name), Config: candidates[name]})
				if h.Ready {
					ready = true
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
			if !ready {
				res.Failed++
				return res, rollback(fmt.Errorf("restored direct instance %s failed desired-state health", name))
			}
		}
		res.Started++
	}
	_ = os.RemoveAll(rollbackDir)

	// Restore the auto-refresh schedule captured in the sidecar.
	if res.AutoRefreshHours > 0 {
		_ = schedule.SetAutoRefresh(res.AutoRefreshHours)
	}

	return res, nil
}
