package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole point of Run returning a Result rather than printing is that this
// file can exist. internal/menu is 1,400 lines with no test because it reads
// stdin and writes stdout directly; these are the same operations with the I/O
// lifted out, so a test drives them by passing a slice and reading a struct.

func TestUnknownAndMissingCommandsSayWhatIsAvailable(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"nonsense"}, {"tunnel"}, {"tunnel", "nonsense"}} {
		r := Run(args)
		if r.Code != CodeUsage {
			t.Errorf("Run(%v) exited %d, want %d for a usage problem", args, r.Code, CodeUsage)
		}
		if r.Out == "" && r.Err == "" {
			t.Errorf("Run(%v) said nothing at all", args)
		}
	}
}

func TestHelpIsNotAnError(t *testing.T) {
	for _, a := range []string{"help", "-h", "--help"} {
		r := Run([]string{a})
		if r.Code != CodeOK {
			t.Errorf("Run(%q) exited %d; asking for help is not a failure", a, r.Code)
		}
		if !strings.Contains(r.Out, "backpack tunnel list") {
			t.Errorf("Run(%q) did not list the commands", a)
		}
	}
}

// The version output is one of the places NOTICE requires the attribution, and
// the JSON form is what a fleet script would read.
func TestVersionCarriesTheAttributionInBothForms(t *testing.T) {
	plain := Run([]string{"version"})
	if plain.Code != CodeOK {
		t.Fatalf("version exited %d", plain.Code)
	}
	if !strings.Contains(plain.Out, "AminMGMT") {
		t.Error("the plain version output does not carry the attribution")
	}

	asJSON := Run([]string{"version", "--json"})
	var got struct {
		Version, Attribution, Source, Licence string
	}
	if err := json.Unmarshal([]byte(asJSON.Out), &got); err != nil {
		t.Fatalf("version --json is not JSON: %v\n%s", err, asJSON.Out)
	}
	if got.Attribution == "" || got.Licence != "AGPL-3.0" {
		t.Errorf("version --json = %+v, want the attribution and the licence", got)
	}
}

// --json is a flag, so it has to work wherever it is written.
func TestTheJSONFlagIsPositionIndependent(t *testing.T) {
	a := Run([]string{"version", "--json"})
	b := Run([]string{"--json", "version"})
	if a.Out != b.Out || a.Code != b.Code {
		t.Errorf("--json before and after the command disagreed:\n%q\nvs\n%q", a.Out, b.Out)
	}
}

// check is the command that did not exist: the engine validates thoroughly and
// does it by exiting, so there was no way to ask whether an edited file is
// sound before restarting a tunnel that currently works.
func TestCheckReportsWhatIsWrongInsteadOfExiting(t *testing.T) {
	dir := t.TempDir()

	good := filepath.Join(dir, "good.toml")
	write(t, good, `[server]
bind_addr = "0.0.0.0:8443"
token = "a-long-enough-token-for-a-test"
ports = ["443=127.0.0.1:2096"]
`)
	if r := Run([]string{"check", "-c", good}); r.Code != CodeOK {
		t.Errorf("a valid config was reported as %d: %s%s", r.Code, r.Out, r.Err)
	}

	bad := filepath.Join(dir, "bad.toml")
	write(t, bad, `[server]
bind_addr = "nonsense"
ports = ["99999"]
`)
	r := Run([]string{"check", "-c", bad})
	if r.Code == CodeOK {
		t.Fatal("a config with a bad bind address, an impossible port and no token passed")
	}
	// Each problem has to be named. A checker that says "invalid" and stops is
	// the engine's exit with extra steps.
	for _, want := range []string{"bind_addr", "ports", "token"} {
		if !strings.Contains(r.Err, want) {
			t.Errorf("the report does not mention %q:\n%s", want, r.Err)
		}
	}

	// A file describing two tunnels at once is the quiet one: the engine picks
	// the first and ignores the rest without a word.
	both := filepath.Join(dir, "both.toml")
	write(t, both, `[server]
bind_addr = "0.0.0.0:8443"
token = "a-long-enough-token-for-a-test"
ports = ["443"]

[l3]
mode = "listen"
addr = "0.0.0.0:9000"
token = "a-long-enough-token-for-a-test"
`)
	if r := Run([]string{"check", "-c", both}); r.Code == CodeOK {
		t.Error("a file describing two kinds of tunnel at once was accepted; the engine " +
			"will run one and silently ignore the other")
	}

	// And a file that is not TOML at all.
	broken := filepath.Join(dir, "broken.toml")
	write(t, broken, "this is not = = toml\n")
	if r := Run([]string{"check", "-c", broken}); r.Code == CodeOK {
		t.Error("a file that does not parse was reported as valid")
	}
}

func TestCheckNeedsExactlyOneFile(t *testing.T) {
	for _, args := range [][]string{{"check"}, {"check", "-c"}, {"check", "a.toml", "b.toml"}} {
		if r := Run(args); r.Code != CodeUsage {
			t.Errorf("Run(%v) exited %d, want %d", args, r.Code, CodeUsage)
		}
	}
}

// The JSON form of check is what a deploy script would gate on.
func TestCheckJSONCarriesTheProblems(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.toml")
	write(t, bad, "[server]\nbind_addr = \"nonsense\"\n")

	r := Run([]string{"check", "-c", bad, "--json"})
	var got struct {
		File     string   `json:"file"`
		OK       bool     `json:"ok"`
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal([]byte(r.Out), &got); err != nil {
		t.Fatalf("check --json is not JSON: %v\n%s", err, r.Out)
	}
	if got.OK || len(got.Problems) == 0 {
		t.Errorf("check --json reported ok=%v with %d problems", got.OK, len(got.Problems))
	}
	if r.Code == CodeOK {
		t.Error("check --json exited 0 for a file it said was not ok")
	}
}

// A tunnel that does not exist is told apart from one that is merely unhealthy,
// because a script wants to treat them differently.
func TestAMissingTunnelIsItsOwnExitCode(t *testing.T) {
	r := Run([]string{"tunnel", "status", "definitely-not-a-tunnel-here"})
	if r.Code != CodeNotFound {
		t.Errorf("status of a missing tunnel exited %d, want %d", r.Code, CodeNotFound)
	}
	if !strings.Contains(r.Err, "definitely-not-a-tunnel-here") {
		t.Errorf("the error does not name what was asked for: %s", r.Err)
	}
}

// list has to work on a machine with no tunnels rather than failing, because
// that is every fresh install.
func TestListOnAMachineWithNoTunnelsIsNotAnError(t *testing.T) {
	r := Run([]string{"tunnel", "list"})
	if r.Code != CodeOK {
		t.Errorf("tunnel list exited %d on this machine: %s", r.Code, r.Err)
	}

	j := Run([]string{"tunnel", "list", "--json"})
	var views []tunnelView
	if err := json.Unmarshal([]byte(j.Out), &views); err != nil {
		t.Fatalf("tunnel list --json is not JSON: %v\n%s", err, j.Out)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("cannot write %s: %v", path, err)
	}
}
