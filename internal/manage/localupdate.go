package manage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// Updating from a release the operator downloaded themselves.
//
// The ordinary update fetches the archive from GitHub, and on the networks this
// project exists to serve that is the step most likely to fail — the mirrors
// help and do not always work. What always works is a browser somewhere else,
// a file, and scp.
//
// So: put backpack_linux_<arch>.tar.gz in /root, choose this, and everything
// after the download is exactly what the online update does — the same
// verification when a checksum is there, the same snapshot before anything is
// touched, the same health check afterwards, and the same automatic rollback
// when a tunnel does not come back. Nothing about the risky half is special
// -cased for being offline.

// localUpdateDirs are where an operator would have put the file. /root first,
// because that is where somebody lands when they scp into a VPS.
func localUpdateDirs() []string { return localUpdateDirsFn() }

// localUpdateDirsFn is the list, behind a variable so a test can point the
// search somewhere that is not this machine's /root.
var localUpdateDirsFn = func() []string {
	return []string{"/root", app.InstallDir, "."}
}

// LocalAssetName is the file this machine can install: the archive for its own
// architecture. An amd64 archive on an arm64 box produces a binary that will
// not execute, and the filename is the only place that is visible before it is
// too late.
func LocalAssetName() string {
	return fmt.Sprintf("backpack_linux_%s.tar.gz", runtime.GOARCH)
}

// LocalUpdate is a release archive found on this machine.
type LocalUpdate struct {
	Path string // where it is
	Size int64
	When time.Time

	// Version is what the binary inside reports, or "" when it could not be
	// asked — an archive from before the version flag existed, or one that is
	// not a Backpack release at all.
	Version string

	// Checksums is the SHA256SUMS file sitting beside it, when there is one.
	Checksums string
}

// FindLocalUpdate looks for a release archive the operator downloaded by hand.
//
// Only the exact filename a release publishes, and only for this machine's
// architecture. Accepting anything looser would mean guessing which of several
// files was meant, on a decision that replaces the binary running every tunnel.
func FindLocalUpdate() (LocalUpdate, bool) {
	name := LocalAssetName()
	for _, dir := range localUpdateDirs() {
		path := filepath.Join(dir, name)
		fi, err := os.Stat(path)
		if err != nil || fi.IsDir() || fi.Size() == 0 {
			continue
		}
		u := LocalUpdate{Path: path, Size: fi.Size(), When: fi.ModTime()}
		if sums := filepath.Join(dir, "SHA256SUMS"); fileExists(sums) {
			u.Checksums = sums
		}
		u.Version = versionInArchive(path)
		return u, true
	}
	return LocalUpdate{}, false
}

// LocalUpdateSearchedIn is where FindLocalUpdate looked, for the message shown
// when it found nothing. An operator who put the file somewhere else needs to
// be told where it was expected, not that it is missing.
func LocalUpdateSearchedIn() []string { return localUpdateDirs() }

// versionInArchive asks the binary inside the archive what it is.
//
// It is extracted to a temporary file and run with -v, which is the only honest
// way to know: nothing in the archive declares a version, and the alternative is
// to install it and find out. A binary that cannot be asked — one from before
// the flag, or a file that is not a release at all — returns "", and the caller
// says so rather than inventing a number.
func versionInArchive(archive string) string {
	tmp, err := os.CreateTemp("", "backpack-check-*")
	if err != nil {
		return ""
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	if err := extractBinaryTo(archive, path); err != nil {
		return ""
	}
	if err := os.Chmod(path, 0755); err != nil {
		return ""
	}
	// Bounded: this is an unknown binary, and one that hangs must not hang the
	// menu that asked.
	out, err := runBounded(path, 5*time.Second, "-v")
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(out)
	if v == "" || len(v) > 32 || strings.ContainsAny(v, "\n\r") {
		return ""
	}
	return v
}

// runBounded runs a command with a deadline and returns its stdout.
func runBounded(bin string, d time.Duration, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return out.String(), err
	case <-time.After(d):
		_ = cmd.Process.Kill()
		<-done
		return "", fmt.Errorf("it did not answer in %s", d)
	}
}

// ApplyLocalUpdate installs a release archive that is already on this machine.
//
// Everything after the download is what ApplyUpdate does, deliberately: the
// snapshot, the health check and the rollback are the parts that make an update
// safe, and an update is not safer for having arrived by scp.
//
// The archive is deleted once the new version is running. It is a hundred-odd
// megabytes on a VPS that usually has twenty gigabytes, and leaving it there
// means the next update finds a stale file and offers to install a version
// older than the one already running.
func ApplyLocalUpdate(u LocalUpdate, logf func(string)) error {
	if logf == nil {
		logf = func(string) {}
	}
	if !fileExists(u.Path) {
		return fmt.Errorf("%s is not there any more", u.Path)
	}

	// Verified when there is something to verify against. Unlike the online
	// path this does not refuse without one: the operator fetched this file
	// themselves and knows where it came from, which is the same standing the
	// checksum list would have had. Said out loud either way.
	if u.Checksums != "" {
		sums, err := os.ReadFile(u.Checksums)
		if err != nil {
			return fmt.Errorf("could not read %s: %w", u.Checksums, err)
		}
		want := hashFor(string(sums), filepath.Base(u.Path))
		if want == "" {
			return fmt.Errorf("%s says nothing about %s — remove it, or download the "+
				"matching one", u.Checksums, filepath.Base(u.Path))
		}
		if err := verifyChecksum(u.Path, want); err != nil {
			return fmt.Errorf("%s does not match %s: %w", filepath.Base(u.Path), u.Checksums, err)
		}
		logf("Checksum verified against " + u.Checksums + ".")
	} else {
		logf("No SHA256SUMS beside the archive — installing it unverified.")
	}

	logf("Taking a safety snapshot...")
	snap, err := TakeSnapshot("pre-update")
	if err != nil {
		return fmt.Errorf("could not take a safety snapshot: %w", err)
	}
	logf("Snapshot saved: " + filepath.Base(snap.Dir))

	what := u.Version
	if what == "" {
		what = filepath.Base(u.Path)
	}
	logf("Installing " + what + "...")
	if err := extractBinary(u.Path); err != nil {
		return err
	}

	_ = os.MkdirAll(app.BackupDir, 0755)
	if InstallPath() == "" {
		_ = os.MkdirAll(app.ConfigDir, 0755)
		_ = os.WriteFile(app.InstallPathFile, []byte(app.InstallDir+"\n"), 0644)
	}

	logf("Restarting services...")
	RestartService(app.WebUIService)
	if err := RestartMonitorService(); err != nil {
		logf("Warning: monitor service could not start: " + err.Error())
	}
	ok, failed := RestartAll()
	logf(fmt.Sprintf("Restarted %d tunnels (%d failed).", ok, failed))

	logf("Checking health...")
	if bad := unhealthyAfterUpdate(); len(bad) > 0 {
		logf("Health check FAILED for: " + strings.Join(bad, ", "))
		logf("Rolling back to the previous version...")
		if rerr := RestoreSnapshot(snap, logf); rerr != nil {
			return fmt.Errorf("update failed AND rollback failed: %v (rollback: %v) — "+
				"restore manually from %s", strings.Join(bad, ", "), rerr, snap.Dir)
		}
		// The archive stays. It is what the operator would reach for to try
		// again, and deleting the file that failed would leave them with a
		// rolled-back server and nothing to retry from.
		return fmt.Errorf("the update failed its health check (%s) — rolled back to %s. "+
			"%s was left in place", strings.Join(bad, ", "), snap.Meta.Version, u.Path)
	}
	logf("Health check passed.")

	// Only now, with the new version running and every tunnel healthy.
	if err := os.Remove(u.Path); err != nil {
		logf("Warning: could not remove " + u.Path + ": " + err.Error())
	} else {
		logf("Removed " + u.Path + ".")
	}
	// And the checksum list with it, when it was this archive's.
	//
	// Left behind it is worse than useless: the next archive dropped in that
	// directory would be checked against sums describing a file that is gone,
	// fail, and read as a corrupted download. Removed only when it names
	// nothing else, so a directory holding both architectures keeps working.
	if u.Checksums != "" {
		if sums, err := os.ReadFile(u.Checksums); err == nil && !namesAnotherFile(string(sums), u) {
			_ = os.Remove(u.Checksums)
		}
	}

	logf("Update complete — now running " + installedVersion() + ".")
	return nil
}

// installedVersion asks the binary that is now in place what it is, so the
// closing line reports what actually happened rather than what this process —
// which is still the old build — was compiled as.
func installedVersion() string {
	out, err := runBounded(app.BinPath, 5*time.Second, "-v")
	if err != nil {
		return "the new version"
	}
	if v := strings.TrimSpace(out); v != "" {
		return v
	}
	return "the new version"
}

// namesAnotherFile reports whether a checksum list still describes a file that
// is present beside it, other than the one just installed.
func namesAnotherFile(sums string, u LocalUpdate) bool {
	dir := filepath.Dir(u.Checksums)
	installed := filepath.Base(u.Path)
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name == installed {
			continue
		}
		if fileExists(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}
