package node

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Taking the fleet key out and putting it back.
//
// The seal has a consequence that has simply not been hit yet: restoring a
// backup onto a different machine brings the fleet list back without the
// credentials to use it, because the key is deliberately not in the archive.
// That is the right answer for a backup that was emailed. It is the wrong one
// for the case it matters most in — the panel machine is gone and this is the
// recovery.
func TestTheFleetKeyCanBeTakenOutAndPutBack(t *testing.T) {
	dir := t.TempDir()
	restore := pointStoreAt(t, dir)
	defer restore()

	// Nothing sealed yet, so there is nothing to keep — and saying so is more
	// useful than handing out a freshly minted key that protects nothing.
	if _, err := ExportSealKey(); err == nil {
		t.Fatal("a machine with no key exported one anyway")
	} else if !strings.Contains(err.Error(), "no fleet key yet") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}

	// Sealing anything creates the key.
	sealed, err := seal("a-root-password")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	exported, err := ExportSealKey()
	if err != nil {
		t.Fatalf("ExportSealKey after sealing: %v", err)
	}
	if exported == "" {
		t.Fatal("the exported key is empty")
	}

	// A fresh machine: the sealed value is unreadable until the key arrives.
	elsewhere := t.TempDir()
	restore2 := pointStoreAt(t, elsewhere)
	defer restore2()

	if got := unseal(sealed); got == "a-root-password" {
		t.Fatal("a sealed password was readable on a machine that does not have the key; " +
			"the seal is protecting nothing")
	}

	if err := ImportSealKey(exported); err != nil {
		t.Fatalf("ImportSealKey: %v", err)
	}
	got := unseal(sealed)
	if got != "a-root-password" {
		t.Errorf("unsealed %q, want the original password", got)
	}
}

// Importing over an existing key would make everything currently sealed here
// unreadable, and there is no undo. It is refused rather than confirmed.
func TestImportingOverAnExistingKeyIsRefused(t *testing.T) {
	dir := t.TempDir()
	restore := pointStoreAt(t, dir)
	defer restore()

	if _, err := seal("something"); err != nil {
		t.Fatalf("seal: %v", err)
	}
	mine, err := ExportSealKey()
	if err != nil {
		t.Fatalf("ExportSealKey: %v", err)
	}

	// The same key is a no-op rather than an error: re-running a recovery step
	// should not punish anybody.
	if err := ImportSealKey(mine); err != nil {
		t.Errorf("importing the key this machine already has was refused: %v", err)
	}

	// A different one is refused, by name.
	other := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("B"), 32))
	err = ImportSealKey(other)
	if err == nil {
		t.Fatal("a different key was written over the existing one; every sealed password " +
			"on this machine would now be unreadable")
	}
	if !strings.Contains(err.Error(), "already has a fleet key") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

func TestAMalformedKeyIsRefused(t *testing.T) {
	restore := pointStoreAt(t, t.TempDir())
	defer restore()

	for _, bad := range []string{"", "not base64 at all!!", "c2hvcnQ="} {
		if err := ImportSealKey(bad); err == nil {
			t.Errorf("ImportSealKey(%q) was accepted", bad)
		}
	}
}

// pointStoreAt aims the registry — and with it the key, which is derived from
// the registry's directory — at a temporary place.
func pointStoreAt(t *testing.T, dir string) func() {
	t.Helper()
	old := StorePath
	StorePath = filepath.Join(dir, "nodes.json")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("cannot prepare %s: %v", dir, err)
	}
	return func() { StorePath = old }
}
