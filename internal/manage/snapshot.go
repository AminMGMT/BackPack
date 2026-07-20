package manage

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// snapshotRetention is how many pre-update snapshots are kept on disk.
const snapshotRetention = 5

// SnapshotMeta describes one snapshot, stored alongside it as meta.json.
type SnapshotMeta struct {
	Stamp   string   `json:"stamp"`   // 20260714-2210
	Version string   `json:"version"` // the version that was running when taken
	Created string   `json:"created"` // RFC3339
	Reason  string   `json:"reason"`  // e.g. "pre-update"
	Tunnels []string `json:"tunnels"` // tunnel names captured
}

// Snapshot is a restorable point-in-time copy of the binary and all configs.
type Snapshot struct {
	Dir  string
	Meta SnapshotMeta
}

// snapshotRoot is where snapshots live (next to the backups, under the
// standard install directory).
func snapshotRoot() string { return app.InstallDir + "/snapshots" }

// TakeSnapshot copies the running binary and the whole config directory into a
// timestamped folder so a failed update or a bad config change can be undone.
func TakeSnapshot(reason string) (Snapshot, error) {
	stamp := time.Now().Format("20060102-150405")
	dir := filepath.Join(snapshotRoot(), reason+"-"+stamp)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Snapshot{}, err
	}

	// 1) The binary currently installed.
	if fileExists(app.BinPath) {
		if err := copyFile(app.BinPath, filepath.Join(dir, "backpack"), 0755); err != nil {
			os.RemoveAll(dir)
			return Snapshot{}, fmt.Errorf("could not snapshot the binary: %w", err)
		}
	}

	// 2) Every config, token, cert and unit-relevant file.
	if err := copyTree(app.ConfigDir, filepath.Join(dir, "config")); err != nil {
		os.RemoveAll(dir)
		return Snapshot{}, fmt.Errorf("could not snapshot the configuration: %w", err)
	}

	var names []string
	for _, t := range List() {
		names = append(names, t.Name)
	}
	meta := SnapshotMeta{
		Stamp:   stamp,
		Version: app.Version,
		Created: time.Now().UTC().Format(time.RFC3339),
		Reason:  reason,
		Tunnels: names,
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0600); err != nil {
		os.RemoveAll(dir)
		return Snapshot{}, err
	}

	pruneSnapshots()
	return Snapshot{Dir: dir, Meta: meta}, nil
}

// ListSnapshots returns every snapshot, newest first.
func ListSnapshots() []Snapshot {
	entries, err := os.ReadDir(snapshotRoot())
	if err != nil {
		return nil
	}
	var out []Snapshot
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(snapshotRoot(), e.Name())
		s := Snapshot{Dir: dir}
		if data, err := os.ReadFile(filepath.Join(dir, "meta.json")); err == nil {
			json.Unmarshal(data, &s.Meta)
		}
		if s.Meta.Stamp == "" {
			// Fall back to the directory name so hand-made folders still list.
			if i := strings.LastIndex(e.Name(), "-"); i > 0 && len(e.Name()) > i+1 {
				s.Meta.Stamp = e.Name()
			} else {
				s.Meta.Stamp = e.Name()
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir > out[j].Dir })
	return out
}

// pruneSnapshots keeps only the newest snapshotRetention snapshots.
func pruneSnapshots() {
	all := ListSnapshots()
	for i := snapshotRetention; i < len(all); i++ {
		os.RemoveAll(all[i].Dir)
	}
}

// RestoreSnapshot puts the binary and configs from a snapshot back in place,
// re-registers a unit for every tunnel it contains and restarts everything.
func RestoreSnapshot(s Snapshot, logf func(string)) error {
	if logf == nil {
		logf = func(string) {}
	}
	if s.Dir == "" || !fileExists(filepath.Join(s.Dir, "meta.json")) {
		return fmt.Errorf("snapshot not found or incomplete: %s", s.Dir)
	}

	// 1) Binary.
	if bin := filepath.Join(s.Dir, "backpack"); fileExists(bin) {
		logf("Restoring the previous binary...")
		tmp := app.BinPath + ".rollback"
		if err := copyFile(bin, tmp, 0755); err != nil {
			return fmt.Errorf("could not restore the binary: %w", err)
		}
		if err := os.Rename(tmp, app.BinPath); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("could not replace the binary: %w", err)
		}
	}

	// 2) Configs.
	if cfg := filepath.Join(s.Dir, "config"); fileExists(cfg) {
		logf("Restoring configuration...")
		if err := copyTree(cfg, app.ConfigDir); err != nil {
			return fmt.Errorf("could not restore the configuration: %w", err)
		}
	}

	// 3) Re-register and restart everything.
	logf("Restarting services...")
	for _, t := range List() {
		_ = writeUnit(t.Name)
	}
	_ = DaemonReload()
	RestartService(app.WebUIService)
	// The monitor runs the binary that was just rolled back, so it has to be
	// restarted too — otherwise a rollback leaves the watchdog and the alerts
	// running the version that failed.
	_ = RestartMonitorService()
	ok, failed := RestartAll()
	logf(fmt.Sprintf("Restarted %d tunnels (%d failed).", ok, failed))
	return nil
}

// --- small file helpers -----------------------------------------------------

// copyFile copies src to dst with the given mode, replacing dst.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// copyTree recursively copies a directory, preserving file permissions.
func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode().Perm())
	}
	return filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target, fi.Mode().Perm())
	})
}
