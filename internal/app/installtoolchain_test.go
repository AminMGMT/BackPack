package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func installerGoFunctions(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	sh := strings.ReplaceAll(string(src), "\r\n", "\n")
	start := strings.Index(sh, "go_new_enough() {")
	build := strings.Index(sh, "build_from_source() {")
	if start < 0 || build < start {
		t.Fatal("installer Go functions missing")
	}
	end := strings.Index(sh[build:], "\n}\n")
	if end < 0 {
		t.Fatal("installer build function missing its end")
	}
	return sh[start : build+end+3]
}

func TestInstallerChecksTheBundledGoPatchVersion(t *testing.T) {
	functions := installerGoFunctions(t)
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"1.26.5", false}, {"1.26.6", true}, {"1.26.10", true},
		{"1.25.99", false}, {"1.27.0", true}, {"1.27rc1", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			// Auto mode pretends to be current; local mode exposes the older compiler.
			fake := `fake_go() { if [[ "$GOTOOLCHAIN" == local ]]; then echo 'go version go` + tc.version + ` linux/amd64'; else echo 'go version go1.26.6 linux/amd64'; fi; }`
			cmd := exec.Command("bash", "-c", "export GOTOOLCHAIN=auto\nGO_VERSION=1.26.6\nGO_MIN_MINOR=26\n"+functions+"\n"+fake+"\ngo_new_enough fake_go")
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.want {
				t.Fatalf("accepted=%v want %v: %s (%v)", err == nil, tc.want, out, err)
			}
		})
	}
}

func TestInstallerBuildsWithTheSelectedGoBinary(t *testing.T) {
	functions := installerGoFunctions(t)
	for _, tc := range []struct {
		name, pathVersion, localVersion, want string
	}{
		{"new-path-old-local", "1.26.6", "1.26.5", "PATH"},
		{"old-path-new-local", "1.26.5", "1.26.6", "LOCAL"},
		{"both-too-old", "1.26.5", "1.26.5", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, fake := range []struct{ name, version, label string }{
				{"path-bin", tc.pathVersion, "PATH"}, {"local-bin", tc.localVersion, "LOCAL"},
			} {
				body := "#!/usr/bin/env bash\n" + `if [[ "$1" == version ]]; then
  if [[ "$GOTOOLCHAIN" == local ]]; then echo 'go version go` + fake.version + ` linux/amd64'; else echo 'go version go1.26.6 linux/amd64'; fi
elif [[ "$1" == build ]]; then
  [[ "$GOTOOLCHAIN" == local ]] || exit 3
  echo 'BUILT_WITH_` + fake.label + `'
else exit 4; fi
`
				if err := os.Mkdir(filepath.Join(dir, fake.name), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, fake.name, "go"), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			// Redirect the system paths before running any installer functions.
			block := strings.NewReplacer("/usr/local/go/bin/go", `"$PWD/local-bin/go"`,
				"/usr/local/go/bin", "$PWD/local-bin", "/etc/backpack/install_path", `"$PWD/install_path"`).Replace(functions)
			prefix := "set -euo pipefail\nexport GOTOOLCHAIN=auto\nGO_VERSION=1.26.6\nGO_MIN_MINOR=26\nSCRIPT_DIR=$PWD\nINSTALL_DIR=$PWD\nBIN_PATH=$PWD/backpack\nexport PATH=\"$PWD/path-bin:$PATH\"\n"
			stubs := "\ninfo() { echo \"$*\"; }; warn() { echo \"$*\"; }; err() { echo \"$*\" >&2; }; download_go() { echo DOWNLOAD_REQUIRED >&2; return 1; };\n"
			cmd := exec.Command("bash", "-c", prefix+block+stubs+"build_from_source")
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if tc.want == "" {
				if err == nil || !strings.Contains(string(out), "DOWNLOAD_REQUIRED") || strings.Contains(string(out), "BUILT_WITH_") {
					t.Fatalf("old compilers must request installation before build: %s (%v)", out, err)
				}
			} else if err != nil || !strings.Contains(string(out), "BUILT_WITH_"+tc.want) {
				t.Fatalf("expected selected %s compiler: %s (%v)", tc.want, out, err)
			}
		})
	}
}
