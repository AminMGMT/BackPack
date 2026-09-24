package optimize

import (
	"os"
	"path/filepath"
	"testing"
)

// WasApplied is what lets an update repair an older Optimize's settings without
// tuning a machine that never asked for it. Getting it backwards either way is
// bad: false on a tuned machine leaves the wide port range in place forever,
// and true on an untouched one retunes a kernel nobody consented to.
func TestWasAppliedFollowsTheFileOptimizeOwns(t *testing.T) {
	dir := t.TempDir()
	old, oldLegacy := sysctlFile, legacySysctlFile
	t.Cleanup(func() { sysctlFile, legacySysctlFile = old, oldLegacy })
	legacySysctlFile = filepath.Join(t.TempDir(), "legacy.conf")

	sysctlFile = filepath.Join(dir, "99-backpack.conf")
	if WasApplied() {
		t.Error("reported as applied with no file present — an update would retune a " +
			"machine whose operator never ran Optimize")
	}

	if err := os.WriteFile(sysctlFile, []byte("# managed by backpack\n"), 0o644); err != nil {
		t.Fatalf("writing the stand-in sysctl file: %v", err)
	}
	if !WasApplied() {
		t.Error("reported as not applied with the file present — a server carrying an " +
			"older Optimize's port range would never be repaired")
	}
}
