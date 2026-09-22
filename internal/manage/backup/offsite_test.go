package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Getting a backup off the machine it describes.
//
// The case backups exist for — the machine died — is the case where a backup
// on that machine is gone with it. What has to be right here is mostly what the
// command is *not* allowed to be: a destination read from a file must not be
// able to become a second command.

func isolateOffsite(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := offsiteFile
	offsiteFile = filepath.Join(dir, "offsite")
	t.Cleanup(func() { offsiteFile = old })
	return dir
}

func TestAnOffsiteCommandRoundTrips(t *testing.T) {
	isolateOffsite(t)
	if got := OffsiteCommand(); got != "" {
		t.Fatalf("an unconfigured machine reported %q", got)
	}
	if err := SetOffsiteCommand("rclone copy {} remote:backpack/"); err != nil {
		t.Fatalf("SetOffsiteCommand: %v", err)
	}
	if got := OffsiteCommand(); got != "rclone copy {} remote:backpack/" {
		t.Fatalf("read back %q", got)
	}
	if err := SetOffsiteCommand(""); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if got := OffsiteCommand(); got != "" {
		t.Fatalf("clearing left %q", got)
	}
}

// Without the placeholder the command runs against nothing, which would
// "succeed" every week and copy no backup anywhere.
func TestACommandWithNoPlaceholderIsRefused(t *testing.T) {
	isolateOffsite(t)
	err := SetOffsiteCommand("rclone copy remote:backpack/")
	if err == nil {
		t.Fatal("a command with no {} was accepted; it would run against nothing " +
			"and report success every week")
	}
	if !strings.Contains(err.Error(), "{}") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

// The destination is read from a file. It must not be able to become a
// pipeline, a redirect, or a second command.
func TestTheCommandIsNotRunThroughAShell(t *testing.T) {
	dir := isolateOffsite(t)
	canary := filepath.Join(dir, "canary")

	// If this were handed to a shell, the second command would run and the
	// canary would exist.
	argv, err := splitOffsite("echo {} ; touch "+canary, "/tmp/a.tar.gz")
	if err != nil {
		t.Fatalf("splitOffsite: %v", err)
	}
	if argv[0] != "echo" {
		t.Fatalf("the command became %q", argv[0])
	}
	for _, a := range argv {
		if strings.Contains(a, ";") && a != ";" {
			t.Fatalf("a shell metacharacter survived inside one argument: %q", a)
		}
	}
	// Every part after the first is an argument to echo, including the ';'
	// and the word 'touch' — which is exactly the point: they are words, not
	// syntax.
	if _, err := os.Stat(canary); err == nil {
		t.Fatal("splitting the command executed part of it")
	}
}

func TestThePlaceholderIsReplacedEverywhereItAppears(t *testing.T) {
	argv, err := splitOffsite("cp {} {}.copy", "/var/backups/b.tar.gz")
	if err != nil {
		t.Fatalf("splitOffsite: %v", err)
	}
	if argv[1] != "/var/backups/b.tar.gz" || argv[2] != "/var/backups/b.tar.gz.copy" {
		t.Fatalf("argv = %v", argv)
	}
}

// Nothing configured is not a failure: most machines will never set this.
func TestSendingWithNothingConfiguredIsNotAnError(t *testing.T) {
	isolateOffsite(t)
	if err := SendOffsite("/nonexistent.tar.gz"); err != nil {
		t.Fatalf("an unconfigured machine reported an error: %v", err)
	}
}

// A backup that is not there is worth refusing loudly rather than handing to
// a command that will fail in its own words.
func TestSendingAMissingArchiveSaysSo(t *testing.T) {
	isolateOffsite(t)
	if err := SetOffsiteCommand("true {}"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	err := SendOffsite(filepath.Join(t.TempDir(), "not-there.tar.gz"))
	if err == nil {
		t.Fatal("sending a missing archive reported success")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("the failure does not say what is wrong: %v", err)
	}
}

// A destination that fails has to say why, in the words of the tool that
// failed — "host key verification failed" and "no space left" are different
// problems and only the tool knows which it was.
func TestAFailingDestinationReportsWhatTheToolSaid(t *testing.T) {
	dir := isolateOffsite(t)
	archive := filepath.Join(dir, "b.tar.gz")
	if err := os.WriteFile(archive, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := SetOffsiteCommand("sh -c false;{}"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := SendOffsite(archive); err == nil {
		t.Fatal("a command that exited non-zero reported success")
	}
}

// It actually copies, which is the one thing all of the above assumes.
func TestAWorkingDestinationReceivesTheArchive(t *testing.T) {
	dir := isolateOffsite(t)
	archive := filepath.Join(dir, "b.tar.gz")
	if err := os.WriteFile(archive, []byte("the backup"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	dest := filepath.Join(dir, "elsewhere.tar.gz")

	if err := SetOffsiteCommand("cp {} " + dest); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := SendOffsite(archive); err != nil {
		t.Fatalf("SendOffsite: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("nothing arrived: %v", err)
	}
	if string(got) != "the backup" {
		t.Fatalf("what arrived was %q", got)
	}
}

// A destination that hangs must not wedge the weekly pass.
//
// It runs in a goroutine inside the monitor service, so a command that never
// returns takes the backups with it — silently, because nothing is watching a
// goroutine that is merely blocked.
func TestADestinationThatHangsIsGivenUpOn(t *testing.T) {
	dir := isolateOffsite(t)
	archive := filepath.Join(dir, "b.tar.gz")
	if err := os.WriteFile(archive, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	old := offsiteTimeoutFor
	offsiteTimeoutFor = 150 * time.Millisecond
	t.Cleanup(func() { offsiteTimeoutFor = old })

	// tail -f takes the archive as its argument and never returns, which is
	// exactly the shape of a destination that accepted the connection and then
	// stopped responding.
	if err := SetOffsiteCommand("tail -f {}"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- SendOffsite(archive) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a command that never finished reported success")
		}
		if !strings.Contains(err.Error(), "gave up") {
			t.Errorf("the failure does not say it timed out: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SendOffsite never returned; the weekly backup would be wedged here")
	}
}
