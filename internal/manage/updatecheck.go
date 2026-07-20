package manage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// Background update checking.
//
// Both the menu and the Telegram bot want to say "a newer version is out", and
// neither can afford to ask GitHub at the moment it needs the answer: the menu
// would stall on every redraw, and from Iran the request may take seconds or
// fail over to the tunnel relay before it succeeds.
//
// So the answer is cached on disk and refreshed in the background. Everything
// on the display path reads the cache and never touches the network.

// updateStateFile holds the last result of a release check.
var updateStateFile = app.ConfigDir + "/update_check.json"

// UpdateState is the cached result of looking for a newer release.
type UpdateState struct {
	// Tag is the newest release tag seen on GitHub, e.g. "v1.6.0".
	Tag string `json:"tag"`
	// Checked is when that answer was obtained.
	Checked time.Time `json:"checked"`
	// Notified is the tag already announced over Telegram, so an available
	// update is mentioned once rather than on every check.
	Notified string `json:"notified"`
}

var updateStateMu sync.Mutex

// loadUpdateState reads the cache, returning a zero value when there is none.
func loadUpdateState() UpdateState {
	updateStateMu.Lock()
	defer updateStateMu.Unlock()
	return loadUpdateStateLocked()
}

func loadUpdateStateLocked() UpdateState {
	var s UpdateState
	data, err := os.ReadFile(updateStateFile)
	if err != nil {
		return s
	}
	json.Unmarshal(data, &s)
	return s
}

func saveUpdateStateLocked(s UpdateState) error {
	// The parent of the file actually being written, not a fixed directory —
	// otherwise this aborts before writing whenever the two differ.
	dir := filepath.Dir(updateStateFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(s, "", "  ")

	// Written to a temporary file and renamed into place.
	//
	// The mutex above only orders writers inside one process, and this file has
	// two: the CLI refreshes it on launch and the monitor service refreshes it
	// on its own timer. A plain WriteFile truncates before it writes, so the
	// other process could read an empty or half-written file and conclude there
	// is no update. Rename is atomic on the same filesystem, so a reader sees
	// either the old contents or the new ones.
	tmp, err := os.CreateTemp(dir, ".update_check-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0644); err != nil {
		return err
	}
	return os.Rename(name, updateStateFile)
}

// UpdateAvailable reports the newer tag, if the cache knows of one. It never
// touches the network, so it is safe to call on every menu redraw.
//
// The comparison is made against the running version each time rather than
// being stored, so the notice disappears by itself once the update is applied.
func UpdateAvailable() (string, bool) {
	s := loadUpdateState()
	if s.Tag == "" || !newerVersion(s.Tag, app.Version) {
		return "", false
	}
	return s.Tag, true
}

// refreshUpdateCheck asks GitHub for the latest release and caches the answer.
// A failure is not recorded: a blocked network should leave the previous answer
// in place rather than erase it.
func refreshUpdateCheck() {
	tag, err := latestTag()
	if err != nil || tag == "" {
		return
	}

	updateStateMu.Lock()
	defer updateStateMu.Unlock()
	s := loadUpdateStateLocked()
	s.Tag = tag
	s.Checked = time.Now()
	saveUpdateStateLocked(s)
}

// RefreshUpdateCheckIfStale refreshes only when the cached answer is older than
// maxAge, so opening the menu repeatedly does not hammer GitHub.
func RefreshUpdateCheckIfStale(maxAge time.Duration) {
	if time.Since(loadUpdateState().Checked) < maxAge {
		return
	}
	refreshUpdateCheck()
}

// MarkUpdateNotified records that this tag has been announced.
func MarkUpdateNotified(tag string) {
	updateStateMu.Lock()
	defer updateStateMu.Unlock()
	s := loadUpdateStateLocked()
	s.Notified = tag
	saveUpdateStateLocked(s)
}

// UpdateNeedsNotifying reports whether there is a newer release that has not
// been announced yet.
func UpdateNeedsNotifying() (string, bool) {
	tag, ok := UpdateAvailable()
	if !ok {
		return "", false
	}
	if loadUpdateState().Notified == tag {
		return "", false
	}
	return tag, true
}
