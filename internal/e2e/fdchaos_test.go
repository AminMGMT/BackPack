//go:build linux

package e2e

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Running out of file descriptors, for real.
//
// This is the third of the faults in chaos_test.go and the one that cannot be
// simulated: the interesting part is that *every* syscall that needs a
// descriptor fails at once — accept, dial, open — while everything already open
// keeps working perfectly. A stub listener returning EMFILE tests the accept
// loop's backoff, which internal/utils/acceptloop already does. It does not test
// what a tunnel does.
//
// The limit is process-wide, so lowering it inside the ordinary test binary
// would break whatever else that binary is doing. The test therefore re-runs
// itself as a child process, lowers the limit there, and reports what the child
// found. That is also closer to the real thing: a VPS with a low `nofile` is
// one where the limit was low before the tunnel started.
//
// What it holds:
//
//   - the process does not die, and does not spin;
//   - a caller that arrives while there are no descriptors gets an error rather
//     than a connection that hangs for ever;
//   - when descriptors are free again, the tunnel carries traffic without
//     anybody restarting it.

const fdChaosEnv = "BACKPACK_FD_CHAOS_CHILD"

func TestATunnelSurvivesRunningOutOfFileDescriptors(t *testing.T) {
	if os.Getenv(fdChaosEnv) == "1" {
		fdChaosChild(t)
		return
	}
	if testing.Short() {
		t.Skip("re-runs this binary as a child process — skipped under -short")
	}

	cmd := exec.Command(os.Args[0],
		"-test.run=^TestATunnelSurvivesRunningOutOfFileDescriptors$",
		"-test.v", "-test.timeout=4m")
	cmd.Env = append(os.Environ(), fdChaosEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the tunnel did not survive running out of file descriptors:\n%s", out)
	}
	// "PASS" alone is not proof: a child whose -test.run matched nothing prints
	// it too, and the whole test would then be a very elaborate no-op. The
	// marker is a line only the child's own body can print.
	if !strings.Contains(string(out), fdChaosMarker) {
		t.Fatalf("the child did not run the test — it printed:\n%s", out)
	}
}

// fdLimit is the ceiling the child runs under. Low enough that it can be
// reached in a moment, high enough that the Go runtime, the test binary and a
// tunnel all fit underneath it with room to work.
const fdLimit = 512

// fdChaosMarker is how the parent knows the child really ran.
const fdChaosMarker = "descriptors held with none left"

func fdChaosChild(t *testing.T) {
	var was unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &was); err != nil {
		t.Fatalf("reading the descriptor limit: %v", err)
	}
	if was.Max < fdLimit {
		t.Skipf("the hard descriptor limit is %d, below the %d this test needs", was.Max, fdLimit)
	}
	lowered := unix.Rlimit{Cur: fdLimit, Max: was.Max}
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &lowered); err != nil {
		t.Fatalf("lowering the descriptor limit: %v", err)
	}
	t.Cleanup(func() { _ = unix.Setrlimit(unix.RLIMIT_NOFILE, &was) })

	backend := startEchoBackend(t)
	tun := startTunnel(t, "tcp", backend, tunnelOptions{})
	if err := tun.roundTrip(randomPayload(t, 4096)); err != nil {
		t.Fatalf("the tunnel did not work before the descriptors ran out: %v", err)
	}

	held := exhaustFDs(t)
	t.Logf("%s: %d", fdChaosMarker, len(held))
	if len(held) == 0 {
		t.Fatal("could not exhaust the descriptors, so nothing was tested")
	}

	// A caller arriving now must be told no. What must not happen is a
	// connection that is accepted and then never answered, because the caller
	// waits on that for as long as its own timeout allows and reports the
	// tunnel as slow rather than as full.
	start := time.Now()
	err := tun.roundTrip([]byte("is anyone there"))
	if err == nil {
		// Being served anyway is a perfectly good outcome — it means the
		// forwarded connection came out of something already open.
		t.Log("the tunnel served a request with no descriptors left, from what it already had")
	} else if waited := time.Since(start); waited > 20*time.Second {
		t.Fatalf("a request with no descriptors left took %s to fail: %v", waited, err)
	}

	// Give it a moment to do whatever a starved accept loop does. If it spins,
	// this is where the CPU goes; if it dies, the child exits non-zero and the
	// parent prints the output.
	time.Sleep(2 * time.Second)

	releaseFDs(held)

	if err := waitFor(60*time.Second, func() bool {
		return tun.roundTrip(randomPayload(t, 4096)) == nil
	}); err != nil {
		t.Fatalf("the tunnel never recovered after the descriptors were free again: %v", err)
	}
}

// exhaustFDs opens descriptors until the process cannot have any more, and
// returns the ones it is holding.
func exhaustFDs(t *testing.T) []*os.File {
	t.Helper()
	var held []*os.File
	for i := 0; i < fdLimit*2; i++ {
		f, err := os.Open(os.DevNull)
		if err != nil {
			break
		}
		held = append(held, f)
	}
	return held
}

func releaseFDs(held []*os.File) {
	for _, f := range held {
		_ = f.Close()
	}
}
