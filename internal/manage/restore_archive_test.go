package manage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type restoreTestEntry struct {
	name string
	body []byte
	kind byte
}

func makeRestoreArchive(t *testing.T, entries ...restoreTestEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		hdr := &tar.Header{Name: entry.name, Typeflag: kind, Mode: 0600, Size: int64(len(entry.body))}
		if kind == tar.TypeDir {
			hdr.Size = 0
			hdr.Mode = 0755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if len(entry.body) > 0 {
			if _, err := tw.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestRestoreArchiveCommitsOnlyAfterValidation(t *testing.T) {
	parent := t.TempDir()
	configDir := filepath.Join(parent, "config")
	if err := os.Mkdir(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "keep.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "install_path"), []byte("local"), 0600); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(restoreMetadata{Version: "test", AutoRefreshHours: 0})
	if err != nil {
		t.Fatal(err)
	}
	archive := makeRestoreArchive(t,
		restoreTestEntry{name: restoreMetadataName, body: meta},
		restoreTestEntry{name: "install_path", body: []byte("remote")},
		restoreTestEntry{name: "tunnel.toml", body: []byte("restored")},
		restoreTestEntry{name: "webui.json", body: []byte("{}")},
	)

	result, err := restoreArchive(bytes.NewReader(archive), configDir, "install_path", "webui.json", "telegram.json")
	if err != nil {
		t.Fatal(err)
	}
	if !result.HasMetadata || result.AutoRefreshHours != 0 || !result.SawConfig || !result.WebUIConfig || result.Files != 2 {
		t.Fatalf("unexpected restore result: %+v", result)
	}
	assertFileContent(t, filepath.Join(configDir, "keep.txt"), "keep")
	assertFileContent(t, filepath.Join(configDir, "install_path"), "local")
	assertFileContent(t, filepath.Join(configDir, "tunnel.toml"), "restored")
	if matches, _ := filepath.Glob(filepath.Join(parent, ".backpack-restore-*")); len(matches) != 0 {
		t.Fatalf("restore transaction left temporary paths: %v", matches)
	}
}

func TestRestoreArchiveRejectsTraversalWithoutChangingConfig(t *testing.T) {
	parent := t.TempDir()
	configDir := filepath.Join(parent, "config")
	if err := os.Mkdir(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "original"), []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := makeRestoreArchive(t, restoreTestEntry{name: "../escaped", body: []byte("bad")})
	if _, err := restoreArchive(bytes.NewReader(archive), configDir, "install_path", "webui.json", "telegram.json"); err == nil {
		t.Fatal("path traversal archive was accepted")
	}
	assertFileContent(t, filepath.Join(configDir, "original"), "safe")
	if _, err := os.Stat(filepath.Join(parent, "escaped")); !os.IsNotExist(err) {
		t.Fatalf("traversal target exists, err=%v", err)
	}
}

func TestRestoreArchiveRejectsBadChecksumWithoutChangingConfig(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	if err := os.Mkdir(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "original"), []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := makeRestoreArchive(t, restoreTestEntry{name: "new.toml", body: []byte("new")})
	archive[len(archive)-1] ^= 0xff
	if _, err := restoreArchive(bytes.NewReader(archive), configDir, "install_path", "webui.json", "telegram.json"); err == nil {
		t.Fatal("archive with an invalid gzip checksum was accepted")
	}
	assertFileContent(t, filepath.Join(configDir, "original"), "safe")
	if _, err := os.Stat(filepath.Join(configDir, "new.toml")); !os.IsNotExist(err) {
		t.Fatalf("corrupt restore modified active config, err=%v", err)
	}
}

func TestRestoreArchiveRejectsLinksAndDuplicates(t *testing.T) {
	for name, entries := range map[string][]restoreTestEntry{
		"symlink": {
			{name: "link", kind: tar.TypeSymlink},
		},
		"duplicate": {
			{name: "same", body: []byte("one")},
			{name: "same", body: []byte("two")},
		},
	} {
		t.Run(name, func(t *testing.T) {
			configDir := filepath.Join(t.TempDir(), "config")
			archive := makeRestoreArchive(t, entries...)
			if _, err := restoreArchive(bytes.NewReader(archive), configDir, "install_path", "webui.json", "telegram.json"); err == nil {
				t.Fatalf("%s archive was accepted", name)
			}
			if _, err := os.Stat(configDir); !os.IsNotExist(err) {
				t.Fatalf("failed restore published a config directory, err=%v", err)
			}
		})
	}
}

func TestSafeRestorePathRejectsCrossPlatformEscapes(t *testing.T) {
	for _, unsafe := range []string{"", ".", "..", "../x", "/absolute", `..\x`, `C:\x`} {
		if _, err := safeRestorePath(unsafe); err == nil {
			t.Errorf("unsafe path %q was accepted", unsafe)
		}
	}
	if got, err := safeRestorePath("nested/config.toml"); err != nil || filepath.ToSlash(got) != "nested/config.toml" {
		t.Fatalf("safe path rejected: got=%q err=%v", got, err)
	}
}

func assertFileContent(t *testing.T, name, want string) {
	t.Helper()
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}
