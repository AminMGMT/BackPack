package manage

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// writeScriptArchive builds a release-shaped archive whose `backpack` is a
// shell script that leaves a marker behind whenever it is run.
func writeScriptArchive(t *testing.T, path, marker string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\ntouch '" + marker + "'\necho v9.9.9\n")
	if err := tw.WriteHeader(&tar.Header{Name: "backpack", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

// The Update menu runs the binary inside a local archive to show its version,
// as root, the moment the menu is drawn. So where it looks has to be somewhere
// nobody else can write: `sudo backpack` keeps the caller's working directory,
// and an archive planted in /tmp by an unprivileged account was executed as
// root. install.sh has refused world-writable directories since BP-011; this
// is the same rule for the other place a local archive is picked up.
func TestALocalArchiveInAWritableDirectoryIsNeitherOfferedNorRun(t *testing.T) {
	for _, mode := range []os.FileMode{0o777, 0o757, 0o1777} {
		dir := t.TempDir()
		marker := filepath.Join(t.TempDir(), "executed")
		writeScriptArchive(t, filepath.Join(dir, LocalAssetName()), marker)
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		withLocalDirs(t, dir)

		if u, ok := FindLocalUpdate(); ok {
			t.Errorf("mode %o: offered %s from a directory others can write", mode, u.Path)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Errorf("mode %o: the binary inside a planted archive was executed", mode)
		}
	}
}

// A directory only this user can write is still searched, and the version is
// still read from it — the feature keeps working where it is safe.
func TestALocalArchiveInAPrivateDirectoryIsStillOffered(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "executed")
	writeScriptArchive(t, filepath.Join(dir, LocalAssetName()), marker)
	withLocalDirs(t, dir)

	u, ok := FindLocalUpdate()
	if !ok {
		t.Fatal("an archive in a private directory was not offered")
	}
	if u.Version != "v9.9.9" {
		t.Fatalf("version = %q, want the one the binary reports", u.Version)
	}
}

// The working directory is not a place an update is taken from at all.
func TestTheWorkingDirectoryIsNotSearched(t *testing.T) {
	for _, d := range localUpdateDirsFn() {
		if d == "." || d == "" {
			t.Fatalf("the search list contains %q, the caller's working directory", d)
		}
	}
}
