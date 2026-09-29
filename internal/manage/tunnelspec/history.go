package tunnelspec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage/core"
)

// What a tunnel's configuration used to be, and when it stopped being that.
//
// Apply already protects against the change that will not start: it writes
// the new config, restarts, waits, and puts the old file back if the tunnel does
// not come up. That covers the loud failure and nothing else.
//
// The quiet one has no cover at all. A configuration that starts perfectly well
// and is simply worse — a preset that suits the link less, a segment cap that is
// too small, a pool that is too large for the memory on the box — leaves nothing
// to go back to. Half an hour later, after three more changes, "what was it
// before I started?" has no answer anywhere on the machine.
//
// So every accepted change files the configuration it replaced. That is one
// store serving two purposes, which is why they are not two stores: the copies
// are what an operator restores from, and the timestamps are what the panel
// draws on the traffic chart, so "did that change help?" stops being a matter of
// impression and becomes a line on a graph.

const (
	// confHistKeep is how many superseded configurations are kept per tunnel.
	// A config is a couple of kilobytes; ten of them is nothing on disk and is
	// more edits than anyone makes between the change that broke something and
	// noticing it did.
	confHistKeep = 10

	// confHistDir is where they live, under the config directory so a backup
	// picks them up with everything else.
	confHistDir = "history"
)

// HistoryRoot is the directory the history lives under. A variable, following
// tunhist.Dir, so a test can point it somewhere safe — app.ConfigDir is a
// constant and a test that wrote to it would write to the real machine.
var HistoryRoot = app.ConfigDir

// ConfigChange is one superseded configuration.
type ConfigChange struct {
	// At is when the change that replaced this configuration was accepted.
	At time.Time `json:"at"`
	// Note says what was being changed, when the caller knew. Empty is fine;
	// the timestamp alone is still worth having.
	Note string `json:"note,omitempty"`
	// Prev is the configuration as it was before, verbatim, so restoring is a
	// copy rather than a re-render — a re-render would quietly drop any key the
	// current spec cannot hold.
	Prev string `json:"prev"`
}

func confHistPath(name string) string {
	return filepath.Join(HistoryRoot, confHistDir, name+".json")
}

// ConfigHistory returns a tunnel's superseded configurations, newest first.
//
// A missing or unreadable file is an empty history, not an error: this is a
// convenience, and no part of running a tunnel may depend on it.
func ConfigHistory(name string) []ConfigChange {
	b, err := os.ReadFile(confHistPath(name))
	if err != nil {
		return nil
	}
	var out []ConfigChange
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// RecordChange files the configuration a change replaced.
//
// Best effort throughout. The change itself has already been made and verified
// by the time this runs, so a failure to write the history must not turn a
// successful edit into a reported one — it only means there is one less thing to
// go back to.
func RecordChange(name string, prev []byte, note string) {
	if len(prev) == 0 {
		return
	}
	dir := filepath.Join(HistoryRoot, confHistDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}

	entries := append([]ConfigChange{{At: time.Now(), Note: note, Prev: string(prev)}},
		ConfigHistory(name)...)
	if len(entries) > confHistKeep {
		entries = entries[:confHistKeep]
	}

	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return
	}
	// The config directory holds tokens; the history holds the same tokens in
	// the copies it keeps, so it is written no more readable than the configs.
	_ = os.WriteFile(confHistPath(name), b, 0o600)
}

// ConfigChangeTimes returns when a tunnel's configuration changed, oldest
// first, for drawing on a chart of the same period.
func ConfigChangeTimes(name string) []int64 {
	h := ConfigHistory(name)
	out := make([]int64, 0, len(h))
	for i := len(h) - 1; i >= 0; i-- {
		out = append(out, h[i].At.Unix())
	}
	return out
}

// RestoreConfigFrom puts back the configuration that was in place before the
// change made at the given moment, and restarts the tunnel on it.
//
// It goes through the same path an edit does, so a restored configuration that
// will not start is reverted exactly like any other change that will not start —
// the undo cannot itself strand the tunnel.
func RestoreConfigFrom(name string, at time.Time) error {
	// Before the name reaches a path: see core.CheckName.
	if err := core.CheckName(name); err != nil {
		return err
	}
	for _, c := range ConfigHistory(name) {
		if !c.At.Equal(at) {
			continue
		}
		return applyRaw(name, []byte(c.Prev), "restored the configuration from "+
			at.Format("2 Jan 15:04"))
	}
	return fmt.Errorf("no configuration from %s is kept for %q", at.Format("2 Jan 15:04"), name)
}

// RenameHistory moves a tunnel's superseded configurations to its new name.
func RenameHistory(oldName, newName string) {
	_ = os.Rename(confHistPath(oldName), confHistPath(newName))
}
