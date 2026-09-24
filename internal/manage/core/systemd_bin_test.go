package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Systemctl runs the binary that exists: systemctl, lower case.
//
// When the function was exported during the package split, the rename reached
// the string inside it too, and every start, stop, restart, enable and
// daemon-reload went to a program called "Systemctl" — which no Linux has, so
// every one of them failed with "executable file not found". Nothing noticed:
// no test ran a real command. This one runs a stand-in named exactly as the
// real one and checks it was reached.
func TestSystemctlRunsTheRealBinaryName(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"stub:$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	out, err := Systemctl("is-active", "backpack-x")
	if err != nil {
		t.Fatalf("Systemctl did not reach a binary named systemctl: %v", err)
	}
	if !strings.Contains(out, "stub:is-active backpack-x") {
		t.Fatalf("Systemctl output = %q", out)
	}
}
