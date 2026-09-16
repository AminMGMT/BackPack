package cmd

import (
	"os"
	"strings"
	"testing"
)

// The engine tunes the kernel on every start, and one of the values it set was
// one this program had already decided was wrong.
//
// Optimize writes ip_local_port_range as 32768-60999 to
// /etc/sysctl.d/99-backpack.conf, the Health Check tells an operator whose
// machine is wide to go and run it, and then ApplyTCPTuning widened it straight
// back to "1024 65535" on the next tunnel start. Optimized, passing its own
// check, and wide again the moment anything restarted — with nothing anywhere
// saying so.
//
// The range is Optimize's to set: it is machine-wide, it decides whether
// unrelated services can keep their own ports, and it is persisted in a file
// this tuning does not write. Nothing in the engine's start path may touch it.
func TestTheEngineDoesNotTouchTheEphemeralPortRange(t *testing.T) {
	src := tuningSource(t)
	if strings.Contains(src, "ip_local_port_range") {
		t.Error("the engine's TCP tuning sets ip_local_port_range again. Optimize owns " +
			"that value and persists it; setting it here undoes the fix on every " +
			"tunnel start and leaves the Health Check advising a remedy that cannot hold")
	}
}

// The rest of the tuning is still expected to be there — a test that passed
// because the whole table had been deleted would be worth nothing.
func TestTheEngineStillAppliesItsOtherTuning(t *testing.T) {
	src := tuningSource(t)
	for _, key := range []string{
		"net.ipv4.tcp_tw_reuse",
		"net.core.somaxconn",
		"net.ipv4.tcp_fastopen",
		"net.core.rmem_max",
	} {
		if !strings.Contains(src, key) {
			t.Errorf("the engine no longer applies %s", key)
		}
	}
}

// tuningSource reads the file rather than the running table because the table is
// built inside a function and never leaves it.
func tuningSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("optimization.go")
	if err != nil {
		t.Fatalf("optimization.go: %v", err)
	}
	// Only what is applied counts; a commented-out line is a note, not a
	// setting, and this file has several of those already.
	var live []string
	for _, line := range strings.Split(string(b), "\n") {
		if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "//") {
			live = append(live, line)
		}
	}
	return strings.Join(live, "\n")
}
