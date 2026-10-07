// Package quota is a tunnel's traffic limit: how many bytes it may carry in
// all, in and out together, before it stops.
//
// The limit is a file beside the tunnel's config, <name>.quota.json, and the
// count it is held against is the tunnel's own metrics total — the one that is
// carried over every restart, reload and update (metrics.NewCollector), and
// restored with a backup. Neither is reset by anything but deleting the
// tunnel, which is the point: a limit that forgot what had been used every
// time the binary was replaced would not be a limit.
//
// The engine enforces it (cmd/quota.go): it watches the total while it runs
// and ends the tunnel the moment the limit is reached, and does not start it
// again until the limit is raised. The panel writes the file and reads it back.
package quota

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Quota is what the file holds.
type Quota struct {
	// Limit is the bytes the tunnel may carry, in and out together. Zero is no
	// limit, which is also what a missing file means.
	Limit uint64 `json:"limit"`
	// Set is when the limit was last changed, for the panel to show.
	Set time.Time `json:"set,omitempty"`
}

// Path is where a tunnel's limit lives.
func Path(dir, name string) string { return filepath.Join(dir, name+".quota.json") }

// Rename preserves the exact limit and timestamp, rejecting orphaned state
// under the destination name instead of silently applying or overwriting it.
func Rename(dir, oldName, newName string) error {
	if _, err := os.Stat(Path(dir, newName)); err == nil {
		return fmt.Errorf("traffic quota for %q already exists", newName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	err := os.Rename(Path(dir, oldName), Path(dir, newName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Load reads a tunnel's limit. No file is no limit; a file that cannot be read
// is reported, because treating it as "no limit" would quietly lift one.
func Load(dir, name string) (Quota, error) {
	var q Quota
	b, err := os.ReadFile(Path(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return q, nil
	}
	if err != nil {
		return q, err
	}
	return q, json.Unmarshal(b, &q)
}

// Save writes a tunnel's limit, or removes the file when there is none.
func Save(dir, name string, limit uint64) error {
	if limit == 0 {
		return Remove(dir, name)
	}
	b, err := json.MarshalIndent(Quota{Limit: limit, Set: time.Now().UTC()}, "", "  ")
	if err != nil {
		return err
	}
	tmp := Path(dir, name) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path(dir, name))
}

// Remove drops a tunnel's limit.
func Remove(dir, name string) error {
	err := os.Remove(Path(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Reached reports whether used has met the limit.
func (q Quota) Reached(used uint64) bool { return q.Limit > 0 && used >= q.Limit }
