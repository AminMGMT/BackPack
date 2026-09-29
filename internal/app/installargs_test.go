package app

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The one-line install the Iran server prints for a kharej without Backpack:
//
//	bash <(curl -fsSL …/install.sh) link apply 'backpack://…'
//
// install.sh has to take exactly that — with or without a leading "backpack" —
// hand it to the installed binary instead of opening the menu, and go on
// refusing anything else. The argument block is run here as the script runs it.
func TestTheInstallerTakesASetupLinkAndNothingElse(t *testing.T) {
	src, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	sh := string(src)
	start := strings.Index(sh, `BP_ARGS=("$@")`)
	end := strings.Index(sh, "# Which release asset this machine can run.")
	if start < 0 || end < start {
		t.Fatal("install.sh no longer has the argument block this test runs")
	}
	block := "err() { echo \"$*\" >&2; }\n" + sh[start:end] + "\nprintf '%s\\n' \"${BP_ARGS[@]}\"\n"

	run := func(args ...string) (string, int) {
		cmd := exec.Command("bash", append([]string{"-c", block, "install.sh"}, args...)...)
		out, err := cmd.Output()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out)), code
	}

	const link = "backpack://H4sIAAAAAAAC_abc"
	for _, args := range [][]string{
		{"link", "apply", link},
		{"backpack", "link", "apply", link},
	} {
		out, code := run(args...)
		if code != 0 || out != "link\napply\n"+link {
			t.Errorf("%q: exit %d, handed on %q", args, code, out)
		}
	}
	if out, code := run(); code != 0 || out != "" {
		t.Errorf("no arguments: exit %d, handed on %q — a plain install must stay plain", code, out)
	}
	for _, args := range [][]string{{"foo"}, {"link"}, {"link", "apply"}, {"node", "--panel", "x"}} {
		if _, code := run(args...); code != 2 {
			t.Errorf("%q was accepted (exit %d)", args, code)
		}
	}

	if !strings.Contains(sh, `exec "$BIN_PATH" "${BP_ARGS[@]}"`) {
		t.Error("install.sh no longer hands the setup link to the installed binary")
	}
}
