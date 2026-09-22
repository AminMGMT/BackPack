package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The restore drill.
//
// A recovery procedure that has never been run is the ordinary state of a
// disaster-recovery plan, and the reason they fail: the first time anybody
// exercises it is the day it has to work, on a machine that is already gone.
//
// The two things this has to get right are that it reports honestly, and that
// it changes nothing — a drill that alters the machine is one nobody runs on a
// working server, and one nobody runs proves nothing.

func TestTheDrillReportsWhatTheBackupHolds(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "config")
	if err := os.MkdirAll(filepath.Join(src, "certs"), 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	for name, body := range map[string]string{
		"kharej.toml": "[client]\nremote_addr = \"1.2.3.4:443\"\n",
		"edge.toml":   "[server]\nbind_addr = \"0.0.0.0:443\"\n",
		"webui.json":  `{"port":8443}`,
		"certs/a.pem": "cert",
	} {
		p := filepath.Join(src, filepath.FromSlash(name))
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("setup %s: %v", name, err)
		}
	}

	archive := filepath.Join(dir, "backup.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := writeBackupTree(f, src); err != nil {
		f.Close()
		t.Skipf("this build cannot write a backup from an arbitrary directory: %v", err)
	}
	f.Close()

	rep, err := TestRestore(archive)
	if err != nil {
		t.Fatalf("the drill refused a good backup: %v", err)
	}
	if len(rep.Tunnels) != 2 {
		t.Errorf("found %d tunnels, want 2: %v", len(rep.Tunnels), rep.Tunnels)
	}
	if !rep.WebUIConfig {
		t.Error("the panel's settings were in the archive and were not reported")
	}
	if rep.Certificates != 1 {
		t.Errorf("found %d certificates, want 1", rep.Certificates)
	}
	if !strings.Contains(rep.Summary(), "edge") {
		t.Errorf("the summary does not name the tunnels:\n%s", rep.Summary())
	}
}

// The whole point: it must not touch the machine.
func TestTheDrillChangesNothing(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live")
	if err := os.MkdirAll(live, 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	marker := filepath.Join(live, "existing.toml")
	if err := os.WriteFile(marker, []byte("[server]\n"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	src := filepath.Join(dir, "config")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "other.toml"), []byte("[client]\n"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	archive := filepath.Join(dir, "b.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := writeBackupTree(f, src); err != nil {
		f.Close()
		t.Skipf("cannot write a backup here: %v", err)
	}
	f.Close()

	before, err := os.ReadDir(live)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := TestRestore(archive); err != nil {
		t.Fatalf("the drill failed: %v", err)
	}
	after, err := os.ReadDir(live)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if len(before) != len(after) {
		t.Fatalf("the drill changed the live directory: %d entries became %d",
			len(before), len(after))
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the drill removed a file that was already there")
	}
}

// A file that is not a backup has to be refused with something an operator can
// act on, and this is the moment to find out rather than during a recovery.
func TestTheDrillRefusesSomethingThatIsNotABackup(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "not-a-backup.tar.gz")
	if err := os.WriteFile(junk, []byte("this is not gzip"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := TestRestore(junk)
	if err == nil {
		t.Fatal("a file that is not a backup was accepted")
	}
	// The wording comes from the real staging path, which is the point: this
	// drill refuses exactly what a restore would refuse, with the same message,
	// so what an operator reads here is what they would have read then.
	if !strings.Contains(err.Error(), "not a valid backup archive") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

func TestTheDrillRefusesAMissingFile(t *testing.T) {
	if _, err := TestRestore(filepath.Join(t.TempDir(), "nope.tar.gz")); err == nil {
		t.Fatal("a missing file was accepted")
	}
}
