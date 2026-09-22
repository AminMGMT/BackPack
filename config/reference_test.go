package config_test

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// The generated configuration reference, and the public surface it describes.
//
// `config/` is the one package in this project that is not `internal/`. Every
// key in it is something an operator has typed into a file on a server they may
// not be able to reach easily, so it is the surface that cannot move quietly.
//
// Two things are held here. The reference must match the declarations, because
// documentation about a configuration file that is wrong is worse than none —
// an operator copies it. And every key must carry an explanation at the
// declaration, because that is the one place it cannot drift from.

// stamp is the trailing generated-on line, which differs every day and is not
// part of what this compares.
var stamp = regexp.MustCompile(`(?s)\n---\n\n\*Generated from.*`)

func TestTheConfigReferenceIsUpToDate(t *testing.T) {
	// From the repository root: the generator reads config/ by a relative path,
	// and `go test` runs in the package's own directory.
	cmd := exec.Command("go", "run", "./tools/configref")
	cmd.Dir = ".."
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("generating the reference: %v\n%s", err, stderr)
	}
	have, err := os.ReadFile("../docs/config-reference.md")
	if err != nil {
		t.Fatalf("reading the committed reference: %v", err)
	}

	want := stamp.ReplaceAllString(string(out), "")
	got := stamp.ReplaceAllString(string(have), "")

	if want != got {
		t.Errorf("docs/config-reference.md no longer matches config/. A key was " +
			"added, renamed, retyped or moved and the reference was not regenerated, " +
			"so it now describes a file that does not exist:\n\n" +
			"    go run ./tools/configref > docs/config-reference.md")
	}
}

// Every key explains itself where it is declared.
//
// The generator marks an undocumented key in the published reference, which is
// the right place for it to be embarrassing — but only if somebody looks. This
// is the part that does not depend on anybody looking.
func TestEveryConfigKeyIsDocumentedAtItsDeclaration(t *testing.T) {
	have, err := os.ReadFile("../docs/config-reference.md")
	if err != nil {
		t.Fatalf("reading the reference: %v", err)
	}
	if n := strings.Count(string(have), "undocumented"); n > 0 {
		t.Errorf("%d configuration key(s) have no comment above them. The reference "+
			"is generated from those comments, so an undocumented key is a key an "+
			"operator has to guess at — and the guess is made on a server they may "+
			"not be able to reach easily.", n)
	}
}

// A sanity check on the generator, so a mistake in it cannot make the two
// tests above pass by comparing nothing with nothing.
func TestTheReferenceActuallyDescribesTheKeys(t *testing.T) {
	have, err := os.ReadFile("../docs/config-reference.md")
	if err != nil {
		t.Fatalf("reading the reference: %v", err)
	}
	s := string(have)
	if n := strings.Count(s, "\n| `"); n < 100 {
		t.Fatalf("the reference lists %d keys; config/ has well over a hundred, so "+
			"the generator has stopped finding them", n)
	}
	// The keys somebody sets on every single tunnel.
	for _, key := range []string{
		"`token`", "`transport`", "`bind_addr`", "`remote_addr`", "`ports`",
		"`fallback_transports`", "`fallback_addrs`",
	} {
		if !strings.Contains(s, key) {
			t.Errorf("%s is missing from the reference", key)
		}
	}
}
