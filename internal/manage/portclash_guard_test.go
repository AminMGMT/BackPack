package manage

import (
	"os"
	"strings"
	"testing"
)

// Every way of making a tunnel has to refuse the same thing. A check that
// covers the panel and not the wizard covers half the product, which is the
// pattern that put four separate faults in this codebase.
//
// The panel's two entry points build their configuration through specFromNew
// rather than performing the check themselves, so this follows that one step:
// the builder does the checking, and each entry point is required to go through
// the builder. Reaching a tunnel on disk by any other route is the fault this
// guards against, whether the route is a new function or a copy of an old one.
func TestEveryCreationPathChecksForAClash(t *testing.T) {
	if !strings.Contains(manageFuncBody(t, "webapi.go", "specFromNew"), "portClash(") {
		t.Error("specFromNew builds a configuration without checking whether it can " +
			"coexist with the tunnels already there")
	}
	for _, fn := range []string{"CreateTunnel", "ApplyTunnel"} {
		if !strings.Contains(manageFuncBody(t, "webapi.go", fn), "specFromNew(") {
			t.Errorf("%s makes a tunnel without going through specFromNew, so nothing "+
				"checks whether it can coexist with the ones already there", fn)
		}
	}
	if !strings.Contains(manageFuncBody(t, "reversesetup.go", "finishSetup"), "portClash(") {
		t.Error("finishSetup creates a tunnel without checking whether it can coexist " +
			"with the ones already there")
	}
}

// manageFuncBody returns the source of one function in this package.
func manageFuncBody(t *testing.T, file, fn string) string {
	t.Helper()
	src := readManageSource(t, file)
	start := strings.Index(src, "func "+fn+"(")
	if start < 0 {
		t.Fatalf("%s is not in %s any more — this guard needs updating", fn, file)
	}
	body := src[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	return body
}

func readManageSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}
