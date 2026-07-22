// Package alerthist keeps a small on-disk record of what the alert watcher has
// fired: the conditions active right now and the most recent messages.
//
// The watcher lives in the backpack-monitor process and the web panel in its
// own; a JSON file is the same decoupling the tunnel metrics already use. The
// monitor writes, everyone else reads, and a missing file simply means nothing
// has ever fired.
package alerthist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// maxEvents bounds the file: only the recent past is worth scrolling through,
// and the full history is in Telegram for anyone who needs it.
const maxEvents = 100

// Event is one alert or recovery message, exactly as it was (or would have
// been) sent to Telegram.
type Event struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

// State is the whole file.
type State struct {
	Updated time.Time `json:"updated"`
	// Active is a human summary of every condition currently firing.
	Active []string `json:"active"`
	// Events holds the newest maxEvents messages, oldest first.
	Events []Event `json:"events"`
}

// Dir is where the file lives; a variable so tests can point it elsewhere.
var Dir = app.ConfigDir

func path() string { return filepath.Join(Dir, "alerts.json") }

// Load reads the recorded state. A missing or unreadable file is an empty
// state, not an error — nothing has fired yet.
func Load() State {
	var st State
	data, err := os.ReadFile(path())
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	return st
}

// RecordEvent appends one event without touching the active conditions — for
// writers other than the alert watcher, like the watchdog announcing a
// restart or the auto-backup announcing an archive.
func RecordEvent(msg string) {
	st := Load()
	st.Events = append(st.Events, Event{Time: time.Now(), Message: msg})
	if len(st.Events) > maxEvents {
		st.Events = st.Events[len(st.Events)-maxEvents:]
	}
	st.Updated = time.Now()
	if data, err := json.MarshalIndent(st, "", "  "); err == nil {
		_ = app.WriteFileAtomic(path(), data, 0o644)
	}
}

// Record appends the messages from one watcher pass and stores the currently
// active conditions. It writes only when something changed, so the quiet
// steady state costs one small read per pass and no writes.
func Record(msgs, active []string) {
	st := Load()
	if len(msgs) == 0 && slices.Equal(active, st.Active) && !st.Updated.IsZero() {
		return
	}
	now := time.Now()
	for _, m := range msgs {
		st.Events = append(st.Events, Event{Time: now, Message: m})
	}
	if len(st.Events) > maxEvents {
		st.Events = st.Events[len(st.Events)-maxEvents:]
	}
	st.Active = active
	st.Updated = now

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	// Atomic, because the panel reads this on a timer while we write it.
	_ = app.WriteFileAtomic(path(), data, 0o644)
}
