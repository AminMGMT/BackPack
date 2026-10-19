//go:build linux

package manage

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The Connection Test between two network namespaces, as root in a user
// namespace, so the direct tunnels run too. Twice: over a clean path, where
// every tunnel has to pass, and over one that cuts every flow after a few
// packets — the route this feature was made for — where the test has to say
// that nothing ordinary holds.
//
//	BP_L3_LIVE=1 go test ./internal/manage -run TestTheConnectionTestAcrossARealPath -v
func TestTheConnectionTestAcrossARealPath(t *testing.T) {
	if os.Getenv("BP_L3_LIVE") == "" {
		t.Skip("set BP_L3_LIVE=1 on a host that allows unprivileged user namespaces to run this")
	}
	bin := filepath.Join(t.TempDir(), "backpack")
	build := exec.Command("go", "build", "-o", bin, "../..")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building the engine: %v", err)
	}
	script, _ := filepath.Abs(filepath.Join("testdata", "conntestlive.sh"))
	self, _ := os.Executable()

	for _, path := range []struct {
		name   string
		filter string
	}{{"clean path", ""}, {"a path that cuts every flow after 12 packets", "12"}} {
		t.Run(path.name, func(t *testing.T) {
			w := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "unshare", "--map-auto", "--map-root-user", "--net", "--mount", "--fork", "--", script)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
			outPath := filepath.Join(w, "script.out")
			f, _ := os.Create(outPath)
			cmd.Stdout, cmd.Stderr = f, f
			cmd.Env = append(os.Environ(), "BIN="+bin, "TESTBIN="+self, "W="+w, "SOAK=15", "FILTER="+path.filter)
			runErr := cmd.Run()
			f.Close()
			out, _ := os.ReadFile(outPath)
			if runErr != nil || !strings.Contains(string(out), "RESULT DONE") {
				t.Fatalf("%v\n%s", runErr, out)
			}
			raw, err := os.ReadFile(filepath.Join(w, "iran.json"))
			if err != nil {
				t.Fatalf("no verdict from the Iran side\n%s", out)
			}
			var results []ConnTestResult
			if err := json.Unmarshal(raw, &results); err != nil {
				t.Fatal(err)
			}
			// Every reverse transport, every direct carrier, and IP spoofing
			// both ways.
			if len(results) != len(connTestReverse)+len(connTestDirect)+2 {
				t.Fatalf("%d results\n%s", len(results), out)
			}
			if !strings.Contains(string(out), "Works Steadily:") {
				t.Errorf("the kharej did not print the verdict\n%s", out)
			}
			for _, r := range results {
				if managedTransport(r.Transport) && r.Status == ctSkipped {
					t.Logf("optional %s skipped: %s", r.Transport, r.Detail)
					continue
				}
				// pck, sni, xdi and the spoofing probes write below netfilter, where
				// the filter here cannot always reach them; every other carrier
				// is an ordinary socket the filter sees.
				raw := r.Transport == "pck" || r.Kind == "spoof" ||
					(r.Kind == "direct" && (r.Transport == "sni" || r.Transport == "xdi"))
				switch {
				case path.filter == "" && r.Status != ctOK:
					t.Errorf("clean path: %s %s is %s (%s) — %d/%d", r.Kind, r.Transport, r.Status, r.Detail, r.OK, r.Tried)
				case path.filter != "" && !raw && r.Status == ctOK:
					t.Errorf("filtered path: %s %s passed; the filter should have cut it", r.Kind, r.Transport)
				}
			}
			t.Log("\n" + ConnTestTable(results))
		})
	}
}

// TestConnTestIranSide is the Iran half of the test above, run by its script
// inside the Iran namespace.
func TestConnTestIranSide(t *testing.T) {
	if os.Getenv("CT_ROLE") != "iran" {
		t.Skip("run by TestTheConnectionTestAcrossARealPath")
	}
	w := os.Getenv("CT_W")
	connTestBinary = func() (string, error) { return os.Getenv("BIN"), nil }
	if n, err := strconv.Atoi(os.Getenv("CT_SOAK")); err == nil {
		connTestSoak = n
	}
	// CT_ONLY=reverse/pck narrows the test to one tunnel, for debugging it.
	if only := os.Getenv("CT_ONLY"); only != "" {
		kind, tr, _ := strings.Cut(only, "/")
		connTestReverse, connTestDirect = nil, nil
		if kind == "reverse" {
			connTestReverse = []string{tr}
		} else {
			connTestDirect = []string{tr}
		}
	}
	s, link, err := StartConnTestIran(ConnTestOptions{Host: os.Getenv("CT_HOST"), Direct: true, SNIDomain: "www.example.com",
		SpoofSrc: "10.10.10.10", Preset: os.Getenv("CT_PRESET")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := os.WriteFile(filepath.Join(w, "link"), []byte(link), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Joined():
	case <-time.After(3 * time.Minute):
		t.Fatal("the kharej never checked in")
	}
	results := s.Run(context.Background(), func(_ int, r ConnTestResult) {
		if r.Status != ctTesting {
			t.Logf("%s %s: %s", r.Kind, r.Transport, r.Status)
		}
	})
	// The engines' logs, for a failure to be read after the test is gone.
	logs, _ := filepath.Glob(filepath.Join(s.dir, "*.log"))
	for _, l := range logs {
		if b, err := os.ReadFile(l); err == nil {
			_ = os.WriteFile(filepath.Join(w, "iran-"+filepath.Base(l)), b, 0o600)
		}
	}
	raw, _ := json.Marshal(results)
	_ = os.WriteFile(filepath.Join(w, "iran.json"), raw, 0o600)
	t.Log("\n" + ConnTestTable(results))
	if os.Getenv("CT_IPV6") == "1" {
		best := s.Best()
		if best.PathMTU != 1400 || best.MSS != 1340 || best.Family != "IPv6" {
			t.Errorf("IPv6 recommendation: %+v", best)
		}
	}
	select {
	case <-s.Fetched():
	case <-time.After(30 * time.Second):
		t.Error("the kharej never collected the verdict")
	}
}

// This runs on CI in isolated network/mount namespaces. The physical link has
// only IPv6, and a 1400-byte MTU proves the measurement uses IPv6's headers and
// DF option rather than merely accepting a literal in a setup form.
func TestTheConnectionTestAcrossAnIPv6OnlyPath(t *testing.T) {
	if os.Getenv("BP_L3_LIVE") == "" {
		t.Skip("set BP_L3_LIVE=1 for namespace integration")
	}
	bin := filepath.Join(t.TempDir(), "backpack")
	build := exec.Command("go", "build", "-o", bin, "../..")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}
	script, _ := filepath.Abs(filepath.Join("testdata", "conntestlive.sh"))
	self, _ := os.Executable()
	w := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	args := []string{"--net", "--mount", "--fork", "--", script}
	if os.Geteuid() != 0 {
		args = append([]string{"--map-auto", "--map-root-user"}, args...)
	}
	cmd := exec.CommandContext(ctx, "unshare", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Env = append(os.Environ(), "BIN="+bin, "TESTBIN="+self, "W="+w, "SOAK=3", "CT_IPV6=1", "PATHMTU=1400", "ONLY=", "FILTER=")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "RESULT DONE") {
		t.Fatalf("IPv6 path: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(w, "iran.json"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var rows []ConnTestResult
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(connTestReverse)+len(connTestDirect)+2 {
		t.Fatalf("missing IPv6 results: %+v", rows)
	}
	for _, r := range rows {
		unsupported := r.Kind == "spoof" || r.Transport == "pck" || r.Transport == "xdi" || r.Transport == "sni"
		if unsupported {
			if r.Status != ctSkipped || !strings.Contains(r.Detail, "IPv6 is not supported") {
				t.Errorf("raw IPv6 verdict: %+v", r)
			}
		} else if managedTransport(r.Transport) && r.Status == ctSkipped {
			t.Logf("optional helper %s: %s", r.Transport, r.Detail)
		} else if r.Status != ctOK {
			t.Errorf("supported IPv6 path failed: %+v", r)
		}
	}
	// The Iran-side helper prints the same recommendation as the CLI, including
	// the measured PMTU and IPv6 MSS (1400 - 40 - 20).
	if !strings.Contains(string(out), "1400") {
		t.Errorf("IPv6 PMTU not reported\n%s", out)
	}
	t.Log(string(out))
}
