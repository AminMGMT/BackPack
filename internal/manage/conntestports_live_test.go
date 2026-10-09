//go:build linux

package manage

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestConnectionTestAvoidsAnOccupiedKharejPortAcrossARealTUN(t *testing.T) {
	if os.Getenv("BP_L3_LIVE") == "" {
		t.Skip("set BP_L3_LIVE=1 on a host with network namespaces and python3")
	}
	bin := os.Getenv("BACKPACK_CURRENT_BINARY")
	if bin == "" {
		bin = filepath.Join(t.TempDir(), "backpack")
		build := exec.Command("go", "build", "-o", bin, "../..")
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			t.Fatal(err)
		}
	}
	self, _ := os.Executable()
	script, _ := filepath.Abs(filepath.Join("testdata", "conntestlive.sh"))
	w := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	args := []string{"--map-auto", "--map-root-user", "--net", "--mount", "--fork", "--", "bash", script}
	if os.Geteuid() == 0 {
		args = []string{"--net", "--mount", "--fork", "--", "bash", script}
	}
	cmd := exec.CommandContext(ctx, "unshare", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Env = append(os.Environ(), "BIN="+bin, "TESTBIN="+self, "W="+w, "SOAK=5", "ONLY=direct/udp", "HOLD_DIRECT_PORT=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "RESULT DONE") {
		t.Fatalf("%v\n%s", err, out)
	}
	held, err := os.ReadFile(filepath.Join(w, "heldport"))
	if err != nil || len(held) == 0 {
		t.Fatal("the original port was not held")
	}
	raw, err := os.ReadFile(filepath.Join(w, "iran.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []ConnTestResult
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Kind == "direct" && row.Transport == "udp" {
			if row.Status != ctOK {
				t.Fatalf("occupied original port prevented the remapped tunnel: %+v\n%s", row, out)
			}
			t.Logf("original Kharej port %s stayed occupied; remapped UDP carried %d/%d echoes and a bulk transfer", held, row.OK, row.Tried)
			return
		}
	}
	t.Fatal("no Direct UDP verdict")
}
