package node

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/app"
	"golang.org/x/crypto/ssh"
)

// The panel and the far server, as two processes.
//
// Everything else in this package answers the panel with answerTo, which echoes
// the request back: it proves what the panel sends, and nothing about what a
// server does with it. Here the far side is a real build of this tree, run as
// `backpack node exec -` behind the SSH server, reading the request from the
// channel's stdin exactly as it does on a managed machine. What crosses between
// them — the command line the panel chooses, base64 on stdin, a Response on
// stdout, the exit status — is the contract, and both halves of it are real.
//
// Only operations that read are sent: this machine is not a managed server,
// and nothing here may change it.

// runFarSide runs command — the one the panel sent — with bin in place of the
// binary it names, wired to the SSH channel.
func runFarSide(ch ssh.Channel, bin, command string) {
	status := uint32(0)
	defer func() { ch.SendRequest("exit-status", false, ssh.Marshal(struct{ S uint32 }{status})) }()
	i := strings.Index(command, " node exec ")
	if i < 0 {
		status = 127
		return
	}
	args := strings.Fields(command[i+1:])
	for j, a := range args {
		args[j] = strings.Trim(a, "'")
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = ch, ch, ch.Stderr()
	if err := cmd.Run(); err != nil {
		status = 1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			status = uint32(ee.ExitCode())
		}
	}
}

// farSideBinary builds this tree once per test binary.
func farSideBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the whole binary; skipped under -short")
	}
	out := filepath.Join(t.TempDir(), "backpack")
	cmd := exec.Command("go", "build", "-o", out, "../..")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("cannot build the far side: %v", err)
	}
	return out
}

func TestThePanelAndARealFarSideAgreeOnTheWire(t *testing.T) {
	isolateStore(t)
	srv := newFakeServer(t, "root", "hunter2")
	srv.binary = farSideBinary(t)
	host, port := srv.addr()
	if _, err := Add("kharej", host, port, "root", "hunter2"); err != nil {
		t.Fatal(err)
	}
	r := NewSSHRunner(nil)
	defer r.Close()

	// hello: the far side describes itself, in the shape the fleet screen reads.
	var info Info
	if err := r.Call("kharej", OpHello, nil, &info); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if info.Version != app.Version || info.OS == "" || info.Arch == "" || info.CPUCores == 0 {
		t.Errorf("hello came back incomplete: %+v", info)
	}

	// A refusal arrives as the far side's own words, not as a broken channel.
	err := r.Call("kharej", OpSettings, NameRequest{Name: "no-such-tunnel-here"}, &struct{}{})
	if err == nil || !strings.Contains(err.Error(), `no tunnel named "no-such-tunnel-here"`) {
		t.Errorf("settings for a tunnel the far side lacks answered %v", err)
	}

	// An operation this build does not have is refused by the far side itself.
	err = r.Call("kharej", "shell", nil, &struct{}{})
	if err == nil || !strings.Contains(err.Error(), errUnknownOp.Error()) {
		t.Errorf("an unknown operation answered %v", err)
	}
	if !r.IsOnline("kharej") {
		t.Error("a refusal marked the server offline; it answered")
	}

	// And the request never rode on the command line.
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for _, cmd := range srv.ran {
		if !strings.HasSuffix(cmd, " node exec -") {
			t.Errorf("the panel ran %q", cmd)
		}
	}
}
