package manage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteBackupProducesCompleteArchive(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "tunnel.toml"), []byte("token = 'secret'\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var archive bytes.Buffer
	meta := archiveMetadata{Version: "test", Created: "now", AutoRefreshHours: 6}
	if err := writeBackupArchive(&archive, root, meta); err != nil {
		t.Fatal(err)
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	entries := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		entries[hdr.Name] = string(data)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(entries[backupMetaName], `"auto_refresh_hours": 6`) {
		t.Fatalf("metadata missing from archive: %q", entries[backupMetaName])
	}
	if entries["nested/tunnel.toml"] != "token = 'secret'\n" {
		t.Fatalf("config file missing from archive: %#v", entries)
	}
}

type failWriter struct {
	remaining int
}

func (w *failWriter) Write(p []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, errors.New("storage failed")
	}
	if len(p) > w.remaining {
		n := w.remaining
		w.remaining = 0
		return n, errors.New("storage failed")
	}
	w.remaining -= len(p)
	return len(p), nil
}

func TestWriteBackupPropagatesFinalWriterErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.toml"), bytes.Repeat([]byte("incompressible-ish-data-"), 100), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeBackupArchive(&failWriter{remaining: 8}, root, archiveMetadata{}); err == nil {
		t.Fatal("backup succeeded after its destination stopped accepting data")
	}
}

func TestWriteBackupRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside-secret")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "linked-secret")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if err := writeBackupArchive(io.Discard, root, archiveMetadata{}); err == nil {
		t.Fatal("backup followed or silently included a symlink")
	}
}

func TestBackupToFilePublishesAtomically(t *testing.T) {
	root := t.TempDir()
	destination := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}

	first, err := publishBackupFile(destination, root, archiveMetadata{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := publishBackupFile(destination, root, archiveMetadata{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("two backups reused the same path %q", first)
	}
	for _, archive := range []string{first, second} {
		info, err := os.Stat(archive)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("backup mode = %o, want 600", info.Mode().Perm())
		}
	}
	matches, err := filepath.Glob(filepath.Join(destination, ".backpack-backup-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary backup files left behind: %v, err=%v", matches, err)
	}
}

func TestBackupFailureLeavesNoPartialArchive(t *testing.T) {
	destination := t.TempDir()
	_, err := publishBackupFile(destination, filepath.Join(t.TempDir(), "missing"), archiveMetadata{}, 10)
	if err == nil {
		t.Fatal("backup of a missing source unexpectedly succeeded")
	}
	entries, readErr := os.ReadDir(destination)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed backup left files behind: %v", entries)
	}
}
