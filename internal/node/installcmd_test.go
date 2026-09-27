package node

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The line the panel runs over SSH to install or upgrade Backpack on a managed
// server has to actually run the installer it downloads.
//
// It was `curl … | bash < /dev/null`. The redirection replaces the pipe as
// bash's stdin, so bash read an empty script and exited 0: every remote
// install and every fleet upgrade reported success having done nothing.
//
// Run for real against a local file:// URL, so the test checks what the shell
// does with the line rather than what the line looks like.
func TestTheInstallCommandRunsTheInstaller(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "install.sh")
	body := "#!/bin/bash\nset -e\n" +
		// The installer takes its quiet path when stdin is not a terminal; it
		// must also not be the script itself, or a `read` would eat the rest.
		"if [ -t 0 ]; then echo tty > " + marker + "; else echo ran > " + marker + "; fi\n" +
		"read -r line || true\n" +
		"echo \"stdin:${line}\" >> " + marker + "\n"
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("sh", "-c", installCommand("file://"+script)).CombinedOutput()
	if err != nil {
		t.Fatalf("the install line failed: %v\n%s", err, out)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the installer never ran: the install line exited 0 and did nothing\n%s", out)
	}
	if !strings.HasPrefix(string(got), "ran\n") {
		t.Fatalf("the installer ran with a terminal on stdin: %q", got)
	}
	if !strings.Contains(string(got), "stdin:\n") {
		t.Fatalf("the installer's stdin was not empty — a read inside it consumed script text: %q", got)
	}
}

// A download that fails must fail the line, not run an empty script and pass.
func TestTheInstallCommandFailsWhenTheDownloadFails(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	missing := "file://" + filepath.Join(t.TempDir(), "no-such-installer.sh")
	if err := exec.Command("sh", "-c", installCommand(missing)).Run(); err == nil {
		t.Fatal("the install line reported success although the installer could not be downloaded")
	}
}

// An upgrade restarts what runs the binary once the installer has replaced it.
// The installer restarts nothing, so an upgrade that stopped there left every
// tunnel on the old build while the server reported the new version.
func TestTheUpgradeCommandRestartsTheTunnelsAfterInstalling(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	installer := filepath.Join(dir, "install.sh")
	bin := filepath.Join(dir, "backpack")
	if err := os.WriteFile(installer, []byte("echo installed >> "+log+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"bin $*\" >> "+log+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("sh", "-c", upgradeCommand("file://"+installer, bin)).CombinedOutput()
	if err != nil {
		t.Fatalf("the upgrade line failed: %v\n%s", err, out)
	}
	got, _ := os.ReadFile(log)
	if string(got) != "installed\nbin --restart-all\n" {
		t.Fatalf("the upgrade did %q; want the installer, then --restart-all", got)
	}
}

// If the installer fails, nothing is restarted onto a half-installed binary.
func TestTheUpgradeCommandStopsWhenTheInstallerFails(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	installer := filepath.Join(dir, "install.sh")
	bin := filepath.Join(dir, "backpack")
	_ = os.WriteFile(installer, []byte("exit 3\n"), 0o644)
	_ = os.WriteFile(bin, []byte("#!/bin/sh\necho \"bin $*\" >> "+log+"\n"), 0o755)

	if err := exec.Command("sh", "-c", upgradeCommand("file://"+installer, bin)).Run(); err == nil {
		t.Fatal("a failing installer did not fail the upgrade")
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("the tunnels were restarted after the installer failed")
	}
}
