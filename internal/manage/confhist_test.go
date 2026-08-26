package manage

import (
	"strings"
	"testing"
	"time"
)

func withTempConfigDir(t *testing.T) {
	t.Helper()
	old := confHistRoot
	confHistRoot = t.TempDir()
	t.Cleanup(func() { confHistRoot = old })
}

// applySpec already reverts a change that will not start. What it could not do
// was answer "what was this before I started fiddling?" — and the dangerous
// change is not the one that fails to start, it is the one that starts fine and
// is simply worse.
func TestASupersededConfigIsKept(t *testing.T) {
	withTempConfigDir(t)

	recordConfigChange("iran-main", []byte("keepalive_period = 75\n"), "")
	got := ConfigHistory("iran-main")
	if len(got) != 1 {
		t.Fatalf("kept %d configurations, want 1", len(got))
	}
	if !strings.Contains(got[0].Prev, "keepalive_period = 75") {
		t.Errorf("the kept configuration is not the one that was replaced: %q", got[0].Prev)
	}
}

// Newest first, and bounded. Ten is more edits than anyone makes between
// breaking something and noticing.
func TestTheHistoryIsNewestFirstAndBounded(t *testing.T) {
	withTempConfigDir(t)

	for i := 0; i < confHistKeep+5; i++ {
		recordConfigChange("iran-main", []byte("version = "+string(rune('a'+i))+"\n"), "")
		time.Sleep(time.Millisecond)
	}

	got := ConfigHistory("iran-main")
	if len(got) != confHistKeep {
		t.Fatalf("kept %d configurations, want the cap of %d", len(got), confHistKeep)
	}
	for i := 1; i < len(got); i++ {
		if got[i].At.After(got[i-1].At) {
			t.Fatalf("entry %d is newer than the one before it — the list is not newest first", i)
		}
	}
	// The oldest ones are the ones dropped.
	if strings.Contains(got[len(got)-1].Prev, "= a") {
		t.Error("the very first configuration survived a full cap of later ones")
	}
}

// An empty previous configuration is not a change worth filing, and writing one
// would put an empty file into the list of things to restore.
func TestAnEmptyPreviousConfigIsNotFiled(t *testing.T) {
	withTempConfigDir(t)

	recordConfigChange("iran-main", nil, "")
	recordConfigChange("iran-main", []byte{}, "")
	if got := ConfigHistory("iran-main"); len(got) != 0 {
		t.Fatalf("filed %d empty configurations", len(got))
	}
}

// The chart needs the moments, oldest first, so they read left to right against
// a series that runs the same way.
func TestChangeTimesRunOldestFirst(t *testing.T) {
	withTempConfigDir(t)

	for i := 0; i < 3; i++ {
		recordConfigChange("iran-main", []byte("n = 1\n"), "")
		time.Sleep(2 * time.Millisecond)
	}

	times := ConfigChangeTimes("iran-main")
	if len(times) != 3 {
		t.Fatalf("got %d moments, want 3", len(times))
	}
	for i := 1; i < len(times); i++ {
		if times[i] < times[i-1] {
			t.Fatal("the moments are not oldest first, so they would draw backwards")
		}
	}
}

// A tunnel nobody has edited has no history, and asking for one must not be an
// error — nothing about running a tunnel may depend on this store.
func TestNoHistoryIsNotAFailure(t *testing.T) {
	withTempConfigDir(t)

	if got := ConfigHistory("never-edited"); got != nil && len(got) != 0 {
		t.Errorf("a tunnel with no history returned %d entries", len(got))
	}
	if got := ConfigChangeTimes("never-edited"); len(got) != 0 {
		t.Errorf("a tunnel with no history returned %d moments", len(got))
	}
	err := RestoreConfigFrom("never-edited", time.Now())
	if err == nil {
		t.Error("restoring from a moment that was never kept reported success")
	}
	if !strings.Contains(err.Error(), "never-edited") {
		t.Errorf("the refusal does not name the tunnel: %v", err)
	}
}
