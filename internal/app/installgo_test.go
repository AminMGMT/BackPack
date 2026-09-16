package app

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The installer has to be able to build what this repository actually is.
//
// install.sh downloads a release binary, and when that fails — a blocked
// GitHub, a rate limit, an architecture with no asset — it falls back to
// building from source. That fallback exists for the servers with the worst
// connectivity, which is to say the ones that can do the least about it.
//
// It builds with GOTOOLCHAIN=local, deliberately: the networks this targets
// frequently cannot reach the toolchain downloader either, and a build that
// silently tries to fetch a compiler is a build that hangs. The consequence is
// that a local Go older than go.mod's `go` line does not fall back to anything.
// It refuses.
//
// So the two numbers have to agree, and for a while they did not: go.mod moved
// to 1.26.0 while the installer went on pinning 1.24.5 and accepting anything
// from 1.24 up. Building from source could not succeed on any machine, and the
// only sign was a compiler error at the end of a long install.
func TestTheInstallerCanBuildThisModule(t *testing.T) {
	sh, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatalf("install.sh: %v", err)
	}
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatalf("go.mod: %v", err)
	}

	wantMinor, ok := goMinor(goDirective(string(mod)))
	if !ok {
		t.Fatalf("go.mod has no usable `go` line")
	}

	// The installer reads go.mod when it is beside it, which is the case for
	// every source build. These constants are what a standalone `curl | bash`
	// falls back to, and they still have to be new enough on their own.
	version := shellValue(string(sh), "GO_VERSION")
	minMinor := shellValue(string(sh), "GO_MIN_MINOR")
	if version == "" || minMinor == "" {
		t.Fatal("install.sh no longer sets GO_VERSION and GO_MIN_MINOR")
	}

	gotMinor, ok := goMinor(version)
	if !ok {
		t.Fatalf("GO_VERSION=%q is not a Go version", version)
	}
	if gotMinor < wantMinor {
		t.Errorf("install.sh downloads Go 1.%d but go.mod needs 1.%d — with "+
			"GOTOOLCHAIN=local that build refuses, and it is the fallback for the "+
			"machines that could not download a release in the first place",
			gotMinor, wantMinor)
	}
	accepts, err := strconv.Atoi(minMinor)
	if err != nil {
		t.Fatalf("GO_MIN_MINOR=%q is not a number", minMinor)
	}
	if accepts < wantMinor {
		t.Errorf("install.sh accepts a Go already on the machine from 1.%d up, but "+
			"go.mod needs 1.%d — so it will keep a toolchain that cannot build this",
			accepts, wantMinor)
	}

	// And it has to actually read go.mod, or the constants above drift again the
	// next time the module moves.
	if !strings.Contains(string(sh), "go.mod") {
		t.Error("install.sh no longer derives the Go version from go.mod, so the two " +
			"will disagree again the next time go.mod moves")
	}
}

// goDirective returns the version on go.mod's `go` line.
func goDirective(mod string) string {
	for _, line := range strings.Split(mod, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "go" {
			return f[1]
		}
	}
	return ""
}

// goMinor pulls the minor number out of "1.26" or "1.26.0".
func goMinor(v string) (int, bool) {
	m := regexp.MustCompile(`^1\.(\d+)`).FindStringSubmatch(strings.TrimPrefix(v, "go"))
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// shellValue reads a top-level NAME="value" or NAME=value assignment. RE2 has
// no backreference, so the quotes are matched and then trimmed.
func shellValue(sh, name string) string {
	m := regexp.MustCompile(`(?m)^` + name + `=("[^"\n]*"|[^\s#]*)`).FindStringSubmatch(sh)
	if m == nil {
		return ""
	}
	return strings.Trim(m[1], `"`)
}
