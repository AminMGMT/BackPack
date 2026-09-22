package cli

import (
	"encoding/json"
	"github.com/backpack/backpack/internal/metrics"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// Traffic in the status output.
//
// The runbook's first question about a tunnel that looks healthy is whether it
// is moving anything, and its second is which direction stopped. Neither could
// be answered from a terminal before this: `state: online` is exactly the
// answer that is wrong in the failure this product cares about most.

func TestHumanBytesReadsAsASize(t *testing.T) {
	for n, want := range map[uint64]string{
		0:                  "0 B",
		512:                "512 B",
		1024:               "1.0 KB",
		1536:               "1.5 KB",
		1024 * 1024:        "1.0 MB",
		1024 * 1024 * 1024: "1.0 GB",
	} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
	// The point of the function: a big number has to read as big. Exact bytes
	// are right in JSON, where something does arithmetic on them, and wrong on
	// a screen, where the question is "is this a lot".
	if got := humanBytes(1503238553); !strings.HasSuffix(got, "GB") {
		t.Errorf("1.5 GB rendered as %q", got)
	}
}

// A snapshot too old to mean anything must be left out entirely rather than
// shown with a caveat: a figure on the screen is read as now.
func TestAStaleSnapshotIsNotReported(t *testing.T) {
	dir := t.TempDir()
	old := stateDir
	stateDir = dir
	t.Cleanup(func() { stateDir = old })

	stale := metrics.Snapshot{
		Name: "t", Taken: time.Now().Add(-trafficWindow - time.Minute),
		BytesIn: 1 << 20, BytesOut: 1 << 20,
	}
	writeSnapshot(t, dir, "t", stale)

	var v tunnelView
	attachTraffic(&v, "t")
	if v.Traffic != nil {
		t.Fatalf("a snapshot %v old was reported as current: %+v", trafficWindow+time.Minute, v.Traffic)
	}
}

func TestAFreshSnapshotIsReportedWithItsAge(t *testing.T) {
	dir := t.TempDir()
	old := stateDir
	stateDir = dir
	t.Cleanup(func() { stateDir = old })

	fresh := metrics.Snapshot{
		Name: "t", Taken: time.Now().Add(-5 * time.Second),
		BytesIn: 4096, BytesOut: 8192, Peer: "1.2.3.4:443",
	}
	writeSnapshot(t, dir, "t", fresh)

	var v tunnelView
	attachTraffic(&v, "t")
	if v.Traffic == nil {
		t.Fatal("a fresh snapshot was not reported")
	}
	if v.Traffic.BytesIn != 4096 || v.Traffic.BytesOut != 8192 {
		t.Errorf("counters = %+v", v.Traffic)
	}
	if v.Traffic.Peer != "1.2.3.4:443" {
		t.Errorf("peer = %q", v.Traffic.Peer)
	}
	// Undated is unusable: the reading has to say how old it is.
	if v.Traffic.Age < 4 || v.Traffic.Age > 30 {
		t.Errorf("age = %ds, want about 5", v.Traffic.Age)
	}
}

// The last hop is the one place that says "the tunnel is fine and every
// connection dies one step past the end of it".
func TestAFailingLastHopIsReportedWithItsShape(t *testing.T) {
	dir := t.TempDir()
	old := stateDir
	stateDir = dir
	t.Cleanup(func() { stateDir = old })

	snap := metrics.Snapshot{
		Name: "t", Taken: time.Now(),
		LocalService: &metrics.LocalServiceState{
			Addr: "127.0.0.1:8080", Why: "refused", Failures: 42,
		},
	}
	writeSnapshot(t, dir, "t", snap)

	var v tunnelView
	attachTraffic(&v, "t")
	// A refusal is a service that is not running; a timeout is usually a
	// firewall on the same machine. The two have different fixes, so the
	// output has to carry which it was — and the address, and how many.
	for _, want := range []string{"127.0.0.1:8080", "refused", "42"} {
		if !strings.Contains(v.LocalService, want) {
			t.Errorf("last hop %q does not mention %q", v.LocalService, want)
		}
	}
}

// A tunnel whose engine has never written a snapshot must report no traffic
// rather than zeroes, which would read as "connected and carrying nothing".
func TestNoSnapshotMeansNoTrafficRatherThanZero(t *testing.T) {
	dir := t.TempDir()
	old := stateDir
	stateDir = dir
	t.Cleanup(func() { stateDir = old })

	var v tunnelView
	attachTraffic(&v, "never-run")
	if v.Traffic != nil {
		t.Fatalf("invented traffic for a tunnel that has never run: %+v", v.Traffic)
	}
}

func writeSnapshot(t *testing.T, dir, name string, s metrics.Snapshot) {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".metrics.json"), data, 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}
}
