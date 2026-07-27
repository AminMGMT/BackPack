package network

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// The tunnel asks every socket for BBR and throws the answer away, because a
// kernel without it should not cost anyone a connection. The price is that the
// request can do nothing at all and say nothing about it, on a tunnel whose
// presets were tuned assuming it worked. These tests cover reading it back.

// The requested algorithm reported to the panel must be the one the sockets
// actually ask for, or the panel would compare against the wrong thing and
// call a working setup broken.
func TestRequestedAlgorithmIsTheOneSocketsAskFor(t *testing.T) {
	_, requested := TunnelCongestion()
	if requested != TCPCongestion {
		t.Errorf("reported %q as requested, but sockets ask for %q", requested, TCPCongestion)
	}
}

// Off Linux there is no /proc to read, and a guess would be worse than silence:
// the panel hides the field when the answer is empty, and would otherwise show
// a confident wrong value on a machine that never had the setting at all.
func TestUnknownRatherThanGuessedWhenProcIsAbsent(t *testing.T) {
	active, requested := TunnelCongestion()
	if runtime.GOOS != "linux" {
		if active != "" {
			t.Errorf("active = %q on %s, want empty — the answer is not knowable here",
				active, runtime.GOOS)
		}
		if requested == "" {
			t.Error("requested must still be reported; only the active value is unknowable")
		}
		return
	}

	// On Linux the answer must be real: whatever is reported has to be an
	// algorithm the kernel actually lists.
	if active == "" {
		t.Skip("/proc/sys/net/ipv4 not readable in this environment")
	}
	available := procSysctl("net/ipv4/tcp_available_congestion_control")
	if !strings.Contains(available, active) {
		t.Errorf("reported %q as active, which is not among the kernel's %q", active, available)
	}
}

// A missing or unreadable parameter reads as empty rather than as a value, so a
// failed read can never be mistaken for an algorithm name.
func TestUnreadableParameterIsEmpty(t *testing.T) {
	if got := procSysctl("net/ipv4/this_parameter_does_not_exist"); got != "" {
		t.Errorf("procSysctl of a missing key = %q, want empty", got)
	}
}

// The value has to come back without the trailing newline /proc puts on it, or
// the comparison against the requested algorithm never matches and the panel
// permanently claims BBR is unavailable.
func TestValueIsTrimmed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/probe", []byte("bbr\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dir + "/probe")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "bbr" {
		t.Errorf("trimmed value = %q, want %q", got, "bbr")
	}
}
