package webui

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The panel's own test suite, run from Go so that `go test ./...` covers it.
//
// The panel is a vanilla-JS single-page application with no build step, which
// is a deliberate choice — `go build` is the whole toolchain and the assets are
// embedded as they are written. The cost of that choice is that nothing checks
// the client side: no bundler to notice an import that names a file which does
// not exist, and no compiler to notice a function that is exported and never
// called. Both of those have happened.
//
// The suite itself is plain `node --test` with no dependencies, no package.json
// and no node_modules, in internal/webui/paneltest. It lives outside panel/
// because `//go:embed panel/js` would otherwise compile the tests into the
// binary and serve them to browsers.
//
// It skips when node is not installed rather than failing: a Go toolchain is
// the only thing this repository requires, and that stays true.
func TestThePanelsOwnTests(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed, so the panel's own tests cannot run here")
	}

	// node --test takes files, not a directory, so the list is expanded here.
	files, err := filepath.Glob(filepath.Join("paneltest", "*.test.js"))
	if err != nil {
		t.Fatalf("looking for the panel's tests: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no panel tests found — this test is looking in the wrong place")
	}

	out, err := exec.Command(node, append([]string{"--test"}, files...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("the panel's tests failed:\n%s", out)
	}
	if !strings.Contains(string(out), "# fail 0") {
		t.Fatalf("could not tell whether the panel's tests passed:\n%s", out)
	}
}
