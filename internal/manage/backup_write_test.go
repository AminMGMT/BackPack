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
	"time"
)

// A backup has to contain the tree it claims to, at the permissions it was
// taken at — a 0600 token file that comes back 0644 is a restore that quietly
// widens access to the tunnel.
func TestBackupArchiveCarriesTheTreeAndItsModes(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]os.FileMode{
		"tunnel.toml":       0600,
		"webui.json":        0644,
		"certs/server.key":  0600,
		"certs/server.crt":  0644,
		"nested/deep/a.txt": 0600,
	})

	var buf bytes.Buffer
	if err := writeBackupTree(&buf, root); err != nil {
		t.Fatalf("writing the archive: %v", err)
	}

	entries := readArchive(t, buf.Bytes())
	if _, ok := entries[backupMetaName]; !ok {
		t.Error("the archive carries no sidecar metadata")
	}
	for name, mode := range map[string]os.FileMode{
		"tunnel.toml":      0600,
		"webui.json":       0644,
		"certs/server.key": 0600,
	} {
		entry, ok := entries[name]
		if !ok {
			t.Errorf("%s is missing from the archive", name)
			continue
		}
		if got := os.FileMode(entry.mode).Perm(); got != mode {
			t.Errorf("%s archived as %v, want %v", name, got, mode)
		}
		if entry.body != name+" contents" {
			t.Errorf("%s archived with %q", name, entry.body)
		}
	}
	if _, ok := entries["nested/deep/a.txt"]; !ok {
		t.Error("a file below the top level is missing from the archive")
	}
}

// The failure this is really about: the tar footer and the gzip trailer are
// written by Close, so an error there used to be swallowed by a deferred call
// and the backup reported success over a truncated archive.
func TestBackupReportsAFailureToFinishWriting(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]os.FileMode{"tunnel.toml": 0600})

	// Fails only once enough has been written that the body is through and
	// the footer and trailer are what remain.
	w := &failAfter{limit: 1}
	err := writeBackupTree(w, root)
	if err == nil {
		t.Fatal("a writer that failed at the end produced no error")
	}
	if !errors.Is(err, errWriterFull) {
		t.Fatalf("error = %v, want it to carry the writer's failure", err)
	}
}

// A symlink produced a header with no target recorded and no content behind
// it, so the archive looked complete and was not.
func TestBackupRefusesWhatItCannotArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]os.FileMode{"tunnel.toml": 0600})
	if err := os.Symlink("/etc/shadow", filepath.Join(root, "sneaky.toml")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := writeBackupTree(io.Discard, root)
	if err == nil {
		t.Fatal("a symlink in the config tree was archived silently")
	}
	if !strings.Contains(err.Error(), "sneaky.toml") {
		t.Fatalf("error = %v, want it to name the offending path", err)
	}
}

// Publishing is atomic: the final name appears only once the archive is whole.
func TestPublishedBackupIsCompleteOrAbsent(t *testing.T) {
	dir := t.TempDir()

	path, err := publishBackup(dir, func(w io.Writer) error {
		_, err := io.WriteString(w, "a complete archive")
		return err
	})
	if err != nil {
		t.Fatalf("publishing: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("published to %s, want a file in %s", path, dir)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the published archive: %v", err)
	}
	if string(body) != "a complete archive" {
		t.Fatalf("published %q", body)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("the archive is %v; it holds every token and key on the host", got)
	}
	if leftovers := partials(t, dir); len(leftovers) != 0 {
		t.Errorf("a successful backup left %v behind", leftovers)
	}
}

// A failed write must leave nothing at all — not a short file under the real
// name, which pruning would count and a restore would half-accept.
func TestFailedBackupLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()

	_, err := publishBackup(dir, func(w io.Writer) error {
		io.WriteString(w, "half an ar")
		return errors.New("disk full")
	})
	if err == nil {
		t.Fatal("a failing write reported success")
	}

	archives, _ := filepath.Glob(filepath.Join(dir, "backpack-backup-*.tar.gz"))
	if len(archives) != 0 {
		t.Errorf("a failed backup published %v", archives)
	}
	if leftovers := partials(t, dir); len(leftovers) != 0 {
		t.Errorf("a failed backup left %v behind", leftovers)
	}
}

// Two backups in the same second used to land on one name, and the second
// replaced the first without saying so.
func TestBackupsInTheSameSecondDoNotReplaceEachOther(t *testing.T) {
	dir := t.TempDir()

	first, err := publishBackup(dir, func(w io.Writer) error {
		_, err := io.WriteString(w, "first")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := publishBackup(dir, func(w io.Writer) error {
		_, err := io.WriteString(w, "second")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if first == second {
		t.Fatal("the second backup took the first one's name")
	}
	assertFileSays(t, first, "first")
	assertFileSays(t, second, "second")

	// Chronological order is what pruning relies on, and it has to hold across
	// the suffixed names too.
	archives, _ := filepath.Glob(filepath.Join(dir, "backpack-backup-*.tar.gz"))
	if len(archives) != 2 {
		t.Fatalf("found %d archives, want 2", len(archives))
	}
}

// Retention still counts whole archives only, and a partial left by a crash is
// eventually swept rather than kept forever.
func TestPruneKeepsTheNewestAndSweepsStalePartials(t *testing.T) {
	dir := t.TempDir()

	for i := 0; i < backupRetention+4; i++ {
		name := filepath.Join(dir, "backpack-backup-2026010"+string(rune('0'+i%10))+"-000000.tar.gz")
		if err := os.WriteFile(name, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	fresh := filepath.Join(dir, ".backpack-backup-fresh.partial")
	stale := filepath.Join(dir, ".backpack-backup-stale.partial")
	for _, p := range []string{fresh, stale} {
		if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * partialAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	pruneBackups(dir)

	archives, _ := filepath.Glob(filepath.Join(dir, "backpack-backup-*.tar.gz"))
	if len(archives) != backupRetention {
		t.Errorf("kept %d archives, want %d", len(archives), backupRetention)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a long-abandoned partial was kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a partial from a backup that may still be running was deleted")
	}
}

// --- helpers ---

var errWriterFull = errors.New("no space left on device")

// failAfter accepts limit writes and then fails, so that the failure lands on
// the footer and trailer rather than on the body.
type failAfter struct {
	limit int
	n     int
}

func (f *failAfter) Write(p []byte) (int, error) {
	f.n++
	if f.n > f.limit {
		return 0, errWriterFull
	}
	return len(p), nil
}

type archiveEntry struct {
	mode int64
	body string
}

func readArchive(t *testing.T, data []byte) map[string]archiveEntry {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the archive is not valid gzip: %v", err)
	}
	defer gz.Close()

	entries := map[string]archiveEntry{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("the archive is not a valid tar: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("reading %s: %v", hdr.Name, err)
		}
		entries[strings.TrimSuffix(hdr.Name, "/")] = archiveEntry{mode: hdr.Mode, body: string(body)}
	}
	return entries
}

func writeTree(t *testing.T, root string, files map[string]os.FileMode) {
	t.Helper()
	for name, mode := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name+" contents"), mode); err != nil {
			t.Fatal(err)
		}
		// WriteFile respects the umask, so say the mode again.
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
}

func partials(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".backpack-backup-*.partial"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func assertFileSays(t *testing.T, path, want string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(body) != want {
		t.Fatalf("%s holds %q, want %q", path, body, want)
	}
}
