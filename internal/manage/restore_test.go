package manage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The behaviour every other test here is really about: a bad archive must not
// have changed anything. Staging is checked by confirming the live tree is
// untouched after each kind of failure.
func TestBadArchivesLeaveTheLiveConfigurationAlone(t *testing.T) {
	tests := []struct {
		name    string
		archive func(t *testing.T) []byte
		wantErr string
	}{
		{
			name:    "path traversal",
			archive: func(t *testing.T) []byte { return archiveOf(t, entry{name: "../../etc/passwd", body: "root::0:0"}) },
			wantErr: "unsafe path",
		},
		{
			name: "traversal after a valid entry",
			archive: func(t *testing.T) []byte {
				return archiveOf(t,
					entry{name: "good.toml", body: "replaced"},
					entry{name: "../escape.toml", body: "nope"},
				)
			},
			wantErr: "unsafe path",
		},
		{
			name:    "absolute path",
			archive: func(t *testing.T) []byte { return archiveOf(t, entry{name: "/etc/shadow", body: "x"}) },
			wantErr: "unsafe path",
		},
		{
			name:    "backslash traversal",
			archive: func(t *testing.T) []byte { return archiveOf(t, entry{name: `a\..\..\escape`, body: "x"}) },
			wantErr: "unsafe path",
		},
		{
			name: "symlink",
			archive: func(t *testing.T) []byte {
				return archiveOf(t, entry{name: "link", typ: tar.TypeSymlink, link: "/etc/shadow"})
			},
			wantErr: "only files and directories",
		},
		{
			name: "hard link",
			archive: func(t *testing.T) []byte {
				return archiveOf(t, entry{name: "link", typ: tar.TypeLink, link: "existing.toml"})
			},
			wantErr: "only files and directories",
		},
		{
			name: "the same name twice",
			archive: func(t *testing.T) []byte {
				return archiveOf(t,
					entry{name: "dup.toml", body: "first"},
					entry{name: "dup.toml", body: "second"},
				)
			},
			wantErr: "more than once",
		},
		{
			name: "corrupt gzip checksum",
			archive: func(t *testing.T) []byte {
				data := archiveOf(t, entry{name: "good.toml", body: "replaced"})
				// The CRC and length live in the last eight bytes. Flipping a
				// bit there is only noticed once the stream is read to the end.
				data[len(data)-5] ^= 0xff
				return data
			},
			wantErr: "corrupt or truncated",
		},
		{
			name: "truncated stream",
			archive: func(t *testing.T) []byte {
				data := archiveOf(t, entry{name: "good.toml", body: strings.Repeat("x", 4096)})
				return data[:len(data)/2]
			},
			wantErr: "",
		},
		{
			name:    "not an archive at all",
			archive: func(t *testing.T) []byte { return []byte("this is not a gzip stream") },
			wantErr: "not a valid backup archive",
		},
		{
			name: "a file over the size limit",
			archive: func(t *testing.T) []byte {
				return archiveOf(t, entry{name: "huge.toml", size: maxRestoreFileBytes + 1, body: "x"})
			},
			wantErr: "over the",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			live := liveConfigDir(t)
			before := snapshot(t, live)

			stage := t.TempDir()
			_, err := stageRestore(bytes.NewReader(tt.archive(t)), live, stage)
			if err == nil {
				t.Fatal("a bad archive was staged without complaint")
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantErr)
			}

			if after := snapshot(t, live); !equalTrees(before, after) {
				t.Fatalf("the live configuration changed despite the failure:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

// A good archive replaces what it names, keeps what it does not, and leaves
// this host's install_path alone.
func TestRestoreStagesAMergeOfTheArchiveAndWhatIsThere(t *testing.T) {
	live := liveConfigDir(t)
	stage := t.TempDir()

	archive := archiveOf(t,
		entry{name: "existing.toml", body: "from the archive"},
		entry{name: "brand-new.toml", body: "new tunnel"},
		entry{name: "install_path", body: "/somewhere/on/the/old/host"},
		entry{name: "certs", typ: tar.TypeDir},
		entry{name: "certs/server.key", body: "key material", mode: 0600},
	)

	contents, err := stageRestore(bytes.NewReader(archive), live, stage)
	if err != nil {
		t.Fatalf("staging a good archive: %v", err)
	}

	assertStaged(t, stage, "existing.toml", "from the archive")
	assertStaged(t, stage, "brand-new.toml", "new tunnel")
	// Absent from the archive, so it survives only because of the seed.
	assertStaged(t, stage, "newer-feature.json", "added by a later version")
	// Machine-specific: the local value wins over the archived one.
	assertStaged(t, stage, "install_path", "/root/BackPack")

	info, err := os.Stat(filepath.Join(stage, "certs/server.key"))
	if err != nil {
		t.Fatalf("the archived key is missing: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("the key staged as %v, want 0600", got)
	}
	if !contents.SawTunnelConfig {
		t.Error("a .toml in the archive was not noticed")
	}
	// existing.toml, brand-new.toml and certs/server.key. The archived
	// install_path was skipped in favour of this host's, so it is not one of
	// the files the restore reports having written.
	if contents.Files != 3 {
		t.Errorf("counted %d restored files, want 3", contents.Files)
	}
}

// Without a local install_path there is nothing to preserve, so the archived
// one is the best guess available.
func TestArchivedInstallPathIsUsedWhenThereIsNoLocalOne(t *testing.T) {
	live := t.TempDir()
	stage := t.TempDir()

	archive := archiveOf(t, entry{name: "install_path", body: "/opt/backpack"})
	if _, err := stageRestore(bytes.NewReader(archive), live, stage); err != nil {
		t.Fatalf("staging: %v", err)
	}
	assertStaged(t, stage, "install_path", "/opt/backpack")
}

// The commit is the only moment the live directory changes, and it either
// happens or it does not.
func TestCommitSwapsTheTreeInOneStep(t *testing.T) {
	parent := t.TempDir()
	live := filepath.Join(parent, "backpack")
	if err := os.Mkdir(live, 0755); err != nil {
		t.Fatal(err)
	}
	writeAt(t, live, "old.toml", "the previous configuration", 0600)

	stage, err := os.MkdirTemp(parent, ".backpack-restore-*")
	if err != nil {
		t.Fatal(err)
	}
	writeAt(t, stage, "new.toml", "the restored configuration", 0600)

	if err := commitRestore(live, stage); err != nil {
		t.Fatalf("committing: %v", err)
	}

	assertStaged(t, live, "new.toml", "the restored configuration")
	if _, err := os.Stat(filepath.Join(live, "old.toml")); !os.IsNotExist(err) {
		t.Error("the previous tree was left mixed in with the restored one")
	}
	if _, err := os.Stat(live + ".restore-previous"); !os.IsNotExist(err) {
		t.Error("the set-aside copy of the previous tree was not cleaned up")
	}

	// The live directory has to stay readable by whatever reads a config.
	info, err := os.Stat(live)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0755 {
		t.Errorf("the config directory came back as %v, want 0755 as it was", got)
	}
}

// Anything in the live tree that the commit would drop is a reason to stop
// before the commit, not a thing to discover afterwards.
func TestRestoreRefusesToSwapOverSomethingItCannotCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	live := liveConfigDir(t)
	if err := os.Symlink("/etc/hosts", filepath.Join(live, "hosts.link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	archive := archiveOf(t, entry{name: "existing.toml", body: "x"})
	_, err := stageRestore(bytes.NewReader(archive), live, t.TempDir())
	if err == nil {
		t.Fatal("a restore would have silently deleted a symlink in the config directory")
	}
	if !strings.Contains(err.Error(), "hosts.link") {
		t.Fatalf("error = %v, want it to name the file it cannot handle", err)
	}
}

func TestSafeRestoreName(t *testing.T) {
	refused := []string{
		"", "..", "../x", "a/../../x", "/abs", "//abs", `a\..\..\x`,
		"a/b/../../../x", ".",
	}
	for _, name := range refused {
		if got, err := safeRestoreName(name); err == nil {
			t.Errorf("safeRestoreName(%q) = %q, want it refused", name, got)
		}
	}

	accepted := map[string]string{
		"tunnel.toml":       "tunnel.toml",
		"certs/server.key":  filepath.Join("certs", "server.key"),
		"./tunnel.toml":     "tunnel.toml",
		"a/b/../c.toml":     filepath.Join("a", "c.toml"),
		"nested/deep/x.txt": filepath.Join("nested", "deep", "x.txt"),
	}
	for name, want := range accepted {
		got, err := safeRestoreName(name)
		if err != nil {
			t.Errorf("safeRestoreName(%q) refused a safe path: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("safeRestoreName(%q) = %q, want %q", name, got, want)
		}
	}
}

// --- helpers ---

type entry struct {
	name string
	body string
	mode int64
	typ  byte
	link string
	size int64 // overrides the header size, to claim more than is delivered
}

func archiveOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0600
		}
		size := int64(len(e.body))
		if e.size != 0 {
			size = e.size
		}
		if typ != tar.TypeReg {
			size = 0
		}
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     mode,
			Size:     size,
			Typeflag: typ,
			Linkname: e.link,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing header for %q: %v", e.name, err)
		}
		if typ == tar.TypeReg && e.size == 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("writing body for %q: %v", e.name, err)
			}
		}
	}

	// A header that claims more than it delivers makes tw.Close complain; the
	// archive bytes are still what the test wants to feed in.
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// liveConfigDir is a stand-in for /etc/backpack with something already in it.
func liveConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeAt(t, dir, "existing.toml", "the configuration that is running", 0600)
	writeAt(t, dir, "newer-feature.json", "added by a later version", 0644)
	writeAt(t, dir, "install_path", "/root/BackPack", 0644)
	writeAt(t, dir, filepath.Join("certs", "old.crt"), "an existing certificate", 0644)
	return dir
}

func writeAt(t *testing.T, dir, name, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertStaged(t *testing.T, dir, name, want string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if string(body) != want {
		t.Fatalf("%s holds %q, want %q", name, body, want)
	}
}

// snapshot records every path and its contents, so a tree can be compared
// against itself after something was supposed to fail.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		if !info.Mode().IsRegular() {
			out[rel] = "<" + info.Mode().Type().String() + ">"
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(body)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return out
}

func equalTrees(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
