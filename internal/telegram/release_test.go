package telegram

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
)

// The release announcement is the one alert with no condition to recover from.
// Everything else here reports a threshold that is crossed and later is not, so
// a repeat is self-limiting; a version stays newer forever, and an announcement
// that does not remember itself becomes a message every check, for good. That
// makes "once, and only once" the property worth holding down.

// stageRelease points the update cache at a temporary file that says tag is the
// newest release. It writes the cache file the alert loop actually reads rather
// than stubbing the lookup, so the test covers the real path — and it never
// touches GitHub or the machine's own state.
func stageRelease(t *testing.T, tag string) {
	t.Helper()
	previous := manage.UpdateStateFile
	manage.UpdateStateFile = t.TempDir() + "/update_check.json"
	t.Cleanup(func() { manage.UpdateStateFile = previous })

	state, err := json.Marshal(manage.UpdateState{Tag: tag, Checked: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manage.UpdateStateFile, state, 0600); err != nil {
		t.Fatalf("staging the update cache: %v", err)
	}
}

func TestNewerReleaseIsAnnouncedOnce(t *testing.T) {
	stageRelease(t, "v999.0.0") // comfortably newer than anything we ship

	first := releaseMessages()
	if len(first) != 1 {
		t.Fatalf("a newer release produced %d messages, want 1", len(first))
	}
	if !strings.Contains(first[0], "v999.0.0") {
		t.Errorf("the announcement does not name the release: %q", first[0])
	}
	if !strings.Contains(first[0], app.Version) {
		t.Errorf("the announcement does not say what is running: %q", first[0])
	}

	// The second pass is the one that matters: the alert loop runs every few
	// seconds, so a message here means a message forever.
	if again := releaseMessages(); len(again) != 0 {
		t.Errorf("the same release was announced twice: %q", again)
	}
}

// A release that is not newer than what is running must say nothing at all,
// or every install would be told to update to the version it already has.
func TestCurrentVersionIsNotAnnounced(t *testing.T) {
	stageRelease(t, app.Version)

	if msgs := releaseMessages(); len(msgs) != 0 {
		t.Errorf("the running version was announced as an update: %q", msgs)
	}
}

// The announcement has to tell the operator what happens if they follow it.
// The reason people leave a tunnel un-updated is the fear of breaking it, so
// the message says the update takes a restore point and rolls itself back.
func TestAnnouncementSaysUpdatingIsRecoverable(t *testing.T) {
	stageRelease(t, "v999.0.0")

	msgs := releaseMessages()
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	for _, want := range []string{"restore point", "rolls back"} {
		if !strings.Contains(msgs[0], want) {
			t.Errorf("the announcement never mentions %q, so it reads as a risk: %q", want, msgs[0])
		}
	}
}
