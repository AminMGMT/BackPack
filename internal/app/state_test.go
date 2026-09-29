package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAMissingStateFileIsEmptyNotAnError(t *testing.T) {
	var v map[string]int
	if err := LoadState(filepath.Join(t.TempDir(), "absent.json"), &v); err != nil {
		t.Fatalf("a missing state file is an error: %v", err)
	}
}

func TestAReadableStateFileLoads(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(p, []byte(`{"a": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var v map[string]int
	if err := LoadState(p, &v); err != nil || v["a"] != 1 {
		t.Fatalf("got %v, %v", v, err)
	}
}

// An unreadable file is moved aside, whole, so a save that follows cannot
// destroy it, and the error names where it went.
func TestAnUnreadableStateFileIsMovedAsideWhole(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	broken := []byte(`{"a": 1,`)
	if err := os.WriteFile(p, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	var v map[string]int
	err := LoadState(p, &v)
	if err == nil {
		t.Fatal("an unreadable file loaded without complaint")
	}
	if _, serr := os.Stat(p); !os.IsNotExist(serr) {
		t.Fatal("the unreadable file is still where the next save would overwrite it")
	}
	kept, _ := filepath.Glob(p + ".unreadable-*")
	if len(kept) != 1 || !strings.Contains(err.Error(), kept[0]) {
		t.Fatalf("kept %v; error %q does not say where", kept, err)
	}
	if got, _ := os.ReadFile(kept[0]); string(got) != string(broken) {
		t.Fatalf("what was kept is %q", got)
	}
}

// A file that parses but has a value of the wrong type — a hand edit, a build
// that changed a field — keeps everything else, and a copy is kept beside it
// so the field the next save drops can still be recovered.
func TestAMistypedValueKeepsTheRestAndACopy(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	body := []byte(`{"a": 1, "b": "not a number"}`)
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	v := map[string]int{}
	if err := LoadState(p, &v); err == nil {
		t.Fatal("a mistyped file loaded without complaint")
	}
	if v["a"] != 1 {
		t.Fatalf("the readable part was thrown away: %v", v)
	}
	if got, _ := os.ReadFile(p); string(got) != string(body) {
		t.Fatal("the original was moved or changed")
	}
	kept, _ := filepath.Glob(p + ".unreadable-*")
	if len(kept) != 1 {
		t.Fatalf("no copy was kept: %v", kept)
	}
	// Read again — as the bot and the panel do, over and over — it is neither
	// copied nor reported again.
	if err := LoadState(p, &v); err != nil {
		t.Fatalf("the same file was reported again: %v", err)
	}
	if kept, _ = filepath.Glob(p + ".unreadable-*"); len(kept) != 1 {
		t.Fatalf("a second copy was made: %v", kept)
	}
}

// A file that does not parse hands back nothing of what the decoder had
// started to fill.
func TestAnUnparsableFileHandsBackNothing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(p, []byte(`{"a": 1, "b": 2`), 0o600); err != nil {
		t.Fatal(err)
	}
	v := map[string]int{}
	_ = LoadState(p, &v)
	if len(v) != 0 {
		t.Fatalf("a half-read state was handed back: %v", v)
	}
}
