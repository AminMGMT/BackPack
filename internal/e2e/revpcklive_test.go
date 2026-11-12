//go:build linux

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The reverse engine over transport = "pck", between two network namespaces.
//
// The rest of this package runs the reverse engine on loopback, which cannot
// carry pck: its segments are built and read through packet sockets on a real
// interface. So this runs it where one can be made, the way the l3 carriers are:
//
//	BP_L3_LIVE=1 go test ./internal/e2e -run TestReversePckOverARealPath -v
//
// It needs what TestL3CarriersOverARealTUN needs (an unprivileged user and
// network namespace, and a build linked with libpcap) and drives
// testdata/revpcklive.sh. The script checks TCP and forwarded UDP, then takes
// each end's interface down and up and requires traffic to come back by itself:
// an interface going down once left a reverse pck tunnel dead for good, its
// receive loop ended by the packet socket's ENETDOWN and its control channel by
// the send that failed.
func TestReversePckOverARealPath(t *testing.T) {
	if os.Getenv("BP_L3_LIVE") == "" {
		t.Skip("set BP_L3_LIVE=1 on a host that allows unprivileged user namespaces to run this")
	}
	for _, tool := range []string{"unshare", "ip", "bc", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is needed and not installed", tool)
		}
	}
	bin := currentBinary(t)
	script, err := filepath.Abs(filepath.Join("testdata", "revpcklive.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// Bounded in time, and its output goes to a file, as in the l3 test.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "unshare", "--map-auto", "--map-root-user", "--net", "--mount", "--fork", "--", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	dir := t.TempDir()
	outFile, err := os.Create(filepath.Join(dir, "script.out"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = outFile, outFile
	cmd.Env = append(os.Environ(), "BIN="+bin, "W="+filepath.Join(dir, "run"))
	err = cmd.Run()
	outFile.Close()
	out := tailOf(filepath.Join(dir, "script.out"), 16<<10)
	last := string(out)
	if i := strings.LastIndex(last, "RESULT "); i >= 0 {
		last = last[i:]
	}
	if err != nil || !strings.HasPrefix(last, "RESULT OK") {
		t.Fatalf("reverse pck: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(strings.TrimPrefix(last, "RESULT OK")))
}
