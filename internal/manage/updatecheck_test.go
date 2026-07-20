package manage

import (
	"testing"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// withTempState points the update cache at a temporary file for the duration of
// one test, so nothing here touches a real installation's state.
func withTempState(t *testing.T) {
	t.Helper()
	orig := updateStateFile
	updateStateFile = t.TempDir() + "/update_check.json"
	t.Cleanup(func() { updateStateFile = orig })
}

func TestNoCacheMeansNoNotice(t *testing.T) {
	withTempState(t)
	if tag, ok := UpdateAvailable(); ok {
		t.Fatalf("a missing cache must not claim an update, got %q", tag)
	}
}

// The running version is compared at read time rather than stored, so the
// notice clears itself once the update has been applied.
func TestSameVersionIsNotAnUpdate(t *testing.T) {
	withTempState(t)
	updateStateMu.Lock()
	saveUpdateStateLocked(UpdateState{Tag: app.Version, Checked: time.Now()})
	updateStateMu.Unlock()

	if tag, ok := UpdateAvailable(); ok {
		t.Fatalf("the running version is not an update, got %q", tag)
	}
}

func TestOlderVersionIsNotAnUpdate(t *testing.T) {
	withTempState(t)
	updateStateMu.Lock()
	saveUpdateStateLocked(UpdateState{Tag: "v0.0.1", Checked: time.Now()})
	updateStateMu.Unlock()

	if tag, ok := UpdateAvailable(); ok {
		t.Fatalf("an older release is not an update, got %q", tag)
	}
}

func TestNewerVersionIsReported(t *testing.T) {
	withTempState(t)
	updateStateMu.Lock()
	saveUpdateStateLocked(UpdateState{Tag: "v99.0.0", Checked: time.Now()})
	updateStateMu.Unlock()

	tag, ok := UpdateAvailable()
	if !ok || tag != "v99.0.0" {
		t.Fatalf("UpdateAvailable() = %q, %v; want v99.0.0, true", tag, ok)
	}
}

// The whole point of persisting "notified": an available update is announced
// once, and stays quiet afterwards even across a restart.
func TestNotifiedOnlyOnce(t *testing.T) {
	withTempState(t)
	updateStateMu.Lock()
	saveUpdateStateLocked(UpdateState{Tag: "v99.0.0", Checked: time.Now()})
	updateStateMu.Unlock()

	tag, ok := UpdateNeedsNotifying()
	if !ok || tag != "v99.0.0" {
		t.Fatalf("first call should want to notify about v99.0.0, got %q, %v", tag, ok)
	}
	MarkUpdateNotified(tag)

	for i := 0; i < 3; i++ {
		if tag, ok := UpdateNeedsNotifying(); ok {
			t.Fatalf("call %d wanted to notify again about %q", i+2, tag)
		}
	}
}

// A newer release after one has already been announced must be announced too.
func TestNewerReleaseAfterNotifyIsAnnounced(t *testing.T) {
	withTempState(t)
	updateStateMu.Lock()
	saveUpdateStateLocked(UpdateState{Tag: "v99.0.0", Checked: time.Now()})
	updateStateMu.Unlock()

	tag, _ := UpdateNeedsNotifying()
	MarkUpdateNotified(tag)

	// A newer one appears.
	updateStateMu.Lock()
	s := loadUpdateStateLocked()
	s.Tag = "v99.1.0"
	saveUpdateStateLocked(s)
	updateStateMu.Unlock()

	got, ok := UpdateNeedsNotifying()
	if !ok || got != "v99.1.0" {
		t.Fatalf("a newer release should be announced, got %q, %v", got, ok)
	}
}

// A failed check must leave the previous answer alone rather than erase it —
// a blocked network is the normal case on this route, not an exception.
func TestFailedRefreshKeepsPreviousAnswer(t *testing.T) {
	if testing.Short() {
		t.Skip("reaches the network — skipped under -short")
	}
	withTempState(t)
	updateStateMu.Lock()
	saveUpdateStateLocked(UpdateState{Tag: "v99.0.0", Checked: time.Now(), Notified: "v98.0.0"})
	updateStateMu.Unlock()

	before := loadUpdateState()

	// latestTag() reaches the network; in a test environment it fails, which is
	// exactly the path under test.
	refreshUpdateCheck()

	after := loadUpdateState()
	if after.Tag != before.Tag && after.Tag == "" {
		t.Fatalf("a failed check erased the cached tag: %q -> %q", before.Tag, after.Tag)
	}
	if after.Notified != before.Notified {
		t.Errorf("a check must not disturb the notified mark: %q -> %q", before.Notified, after.Notified)
	}
}

func TestRefreshIfStaleSkipsFreshAnswers(t *testing.T) {
	withTempState(t)
	fresh := time.Now()
	updateStateMu.Lock()
	saveUpdateStateLocked(UpdateState{Tag: "v99.0.0", Checked: fresh})
	updateStateMu.Unlock()

	RefreshUpdateCheckIfStale(time.Hour)

	// A skipped refresh leaves the timestamp untouched.
	if got := loadUpdateState().Checked; !got.Equal(fresh.Truncate(0)) && got.Sub(fresh).Abs() > time.Second {
		t.Errorf("a fresh answer should not have been refreshed (checked moved from %v to %v)", fresh, got)
	}
}
