package manage

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Installing a release the operator downloaded themselves.
//
// The download is the step that fails on the networks this project exists to
// serve, and it is the only step that has to happen online. So it can be done
// somewhere else: put the archive on the machine, and everything after it —
// the checksum, the snapshot, the health check, the rollback — is exactly what
// the online update does.

// withLocalDirs points the search at a temp directory.
func withLocalDirs(t *testing.T, dirs ...string) {
	t.Helper()
	old := localUpdateDirsFn
	localUpdateDirsFn = func() []string { return dirs }
	t.Cleanup(func() { localUpdateDirsFn = old })
}

// The archive is found only under the exact name a release publishes, for this
// machine's architecture.
func TestOnlyThisMachinesArchiveIsOffered(t *testing.T) {
	dir := t.TempDir()
	withLocalDirs(t, dir)

	if _, ok := FindLocalUpdate(); ok {
		t.Fatal("found an update in an empty directory")
	}

	// Names that are not the release asset for this machine.
	other := "arm64"
	if runtime.GOARCH == "arm64" {
		other = "amd64"
	}
	for _, name := range []string{
		"backpack.tar.gz",
		"backpack_linux_" + other + ".tar.gz",
		"backpack_linux_" + runtime.GOARCH + ".tgz",
		"Backpack_linux_" + runtime.GOARCH + ".tar.gz",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if u, ok := FindLocalUpdate(); ok {
		t.Errorf("offered %s — an archive for another architecture produces a binary "+
			"that will not execute, and the filename is the last place that is visible",
			u.Path)
	}

	// The right one is found.
	want := filepath.Join(dir, LocalAssetName())
	if err := os.WriteFile(want, []byte("not really an archive"), 0644); err != nil {
		t.Fatal(err)
	}
	u, ok := FindLocalUpdate()
	if !ok {
		t.Fatal("did not find the release archive for this machine")
	}
	if u.Path != want {
		t.Errorf("found %s, want %s", u.Path, want)
	}
	// It is not a real archive, so nothing is claimed about its version.
	if u.Version != "" {
		t.Errorf("claimed version %q for a file that is not a release", u.Version)
	}
}

// An empty file is not an update. It is what a failed scp leaves behind.
func TestAnEmptyFileIsNotOffered(t *testing.T) {
	dir := t.TempDir()
	withLocalDirs(t, dir)
	if err := os.WriteFile(filepath.Join(dir, LocalAssetName()), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok := FindLocalUpdate(); ok {
		t.Error("a zero-byte file was offered as an update")
	}
}

// A checksum list beside the archive is noticed, and used.
func TestTheChecksumListBesideItIsUsed(t *testing.T) {
	dir := t.TempDir()
	withLocalDirs(t, dir)

	archive := filepath.Join(dir, LocalAssetName())
	if err := os.WriteFile(archive, []byte("pretend release"), 0644); err != nil {
		t.Fatal(err)
	}
	sums := filepath.Join(dir, "SHA256SUMS")
	if err := os.WriteFile(sums, []byte("0000  "+LocalAssetName()+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	u, ok := FindLocalUpdate()
	if !ok {
		t.Fatal("did not find the archive")
	}
	if u.Checksums != sums {
		t.Fatalf("checksum list = %q, want %q", u.Checksums, sums)
	}

	// A hash that does not match must stop the install before anything is
	// touched, and must leave the archive where it is — it is what the operator
	// would look at to work out what went wrong.
	err := ApplyLocalUpdate(u, nil)
	if err == nil {
		t.Fatal("installed an archive whose checksum did not match")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
	if _, serr := os.Stat(archive); serr != nil {
		t.Error("the archive was deleted after a failed verification — it is the " +
			"one thing the operator would look at")
	}
}

// A checksum list that says nothing about this file is an error, not a silent
// unverified install: it means the two files came from different releases.
func TestAChecksumListThatDoesNotNameTheArchiveIsRefused(t *testing.T) {
	dir := t.TempDir()
	withLocalDirs(t, dir)

	if err := os.WriteFile(filepath.Join(dir, LocalAssetName()), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"),
		[]byte("0000  something_else.tar.gz\n"), 0644); err != nil {
		t.Fatal(err)
	}
	u, _ := FindLocalUpdate()
	err := ApplyLocalUpdate(u, nil)
	if err == nil || !strings.Contains(err.Error(), "says nothing about") {
		t.Errorf("a mismatched checksum list was not reported: %v", err)
	}
}

// The checksum list is only cleaned up when it describes nothing else.
func TestAChecksumListCoveringAnotherArchiveIsKept(t *testing.T) {
	dir := t.TempDir()
	other := "backpack_linux_arm64.tar.gz"
	if runtime.GOARCH == "arm64" {
		other = "backpack_linux_amd64.tar.gz"
	}
	if err := os.WriteFile(filepath.Join(dir, other), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	sums := filepath.Join(dir, "SHA256SUMS")
	u := LocalUpdate{Path: filepath.Join(dir, LocalAssetName()), Checksums: sums}

	body := "aaa  " + LocalAssetName() + "\nbbb  " + other + "\n"
	if !namesAnotherFile(body, u) {
		t.Error("a checksum list covering the other architecture's archive would be " +
			"removed, and that archive would then fail to verify")
	}

	// With nothing else beside it, it goes.
	os.Remove(filepath.Join(dir, other))
	if namesAnotherFile(body, u) {
		t.Error("a checksum list naming only files that are gone was kept")
	}
}

// The message when there is nothing to install has to say where to put it.
func TestTheOperatorIsToldWhereToPutTheFile(t *testing.T) {
	dirs := LocalUpdateSearchedIn()
	if len(dirs) == 0 {
		t.Fatal("nowhere is searched")
	}
	if dirs[0] != "/root" {
		t.Errorf("the first place searched is %q — /root is where somebody lands "+
			"when they scp into a VPS, and it is what the instructions say", dirs[0])
	}
	if !strings.HasPrefix(LocalAssetName(), "backpack_linux_") ||
		!strings.HasSuffix(LocalAssetName(), ".tar.gz") {
		t.Errorf("the asset name %q is not what a release publishes", LocalAssetName())
	}
	if !strings.Contains(LocalAssetName(), runtime.GOARCH) {
		t.Errorf("the asset name %q does not name this machine's architecture", LocalAssetName())
	}
}
