package app

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteFileAtomicWritesContentAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	want := []byte(`{"token":"secret"}`)

	if err := WriteFileAtomic(path, want, 0600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("content = %q, want %q", got, want)
	}
	// A file holding a token must not be world-readable.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("mode = %v, want 0600 — CreateTemp's default was not overridden correctly", perm)
	}
}

func TestWriteFileAtomicCreatesParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deep", "cfg.json")
	if err := WriteFileAtomic(path, []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFileAtomic should create missing parents: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file was not created: %v", err)
	}
}

func TestWriteFileAtomicReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := WriteFileAtomic(path, []byte("old contents, longer"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("content = %q, want %q — a shorter rewrite left the old tail behind", got, "new")
	}
}

// A reader must never observe a partially written file. This is the whole point
// of the helper: the CLI writes these configs while the monitor service reads
// them on a timer, and a plain truncate-then-write leaves a window where the
// file is empty.
func TestWriteFileAtomicNeverExposesPartialContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")

	small := []byte("{}")
	large := bytes.Repeat([]byte("A"), 256*1024)

	if err := WriteFileAtomic(path, small, 0644); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Reader: every read must see one of the two complete versions.
	var bad int
	var mu sync.Mutex
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			got, err := os.ReadFile(path)
			if err != nil {
				continue // the file always exists, but a rename can race the open
			}
			if !bytes.Equal(got, small) && !bytes.Equal(got, large) {
				mu.Lock()
				bad++
				mu.Unlock()
			}
		}
	}()

	// Writer: alternate between the two sizes.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			payload := large
			if i%2 == 0 {
				payload = small
			}
			if err := WriteFileAtomic(path, payload, 0644); err != nil {
				t.Errorf("write %d: %v", i, err)
				return
			}
		}
		close(stop)
	}()

	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if bad > 0 {
		t.Errorf("%d reads saw a partially written file", bad)
	}
}

// The temporary file must not survive, or the config directory slowly fills
// with .tmp litter that the backup then sweeps up too.
func TestWriteFileAtomicLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")

	for i := 0; i < 5; i++ {
		if err := WriteFileAtomic(path, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %d files (%v), want only the target", len(entries), names)
	}
}
