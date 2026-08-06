package manage

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

const setupFlowHelperEnv = "BACKPACK_SETUP_FLOW_HELPER"

// The helper runs in a child process so tui's package-level stdin reader is
// attached to the pipe from process start, exactly like a real terminal run.
func TestSetupFlowHelper(t *testing.T) {
	if os.Getenv(setupFlowHelperEnv) != "1" {
		return
	}
	SetupServer()
}

func runSetupFlow(t *testing.T, input string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSetupFlowHelper$")
	cmd.Env = append(os.Environ(), setupFlowHelperEnv+"=1")
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("setup helper failed: %v\n%s", err, out)
	}
	return string(out)
}

func TestEveryConcreteTransportShowsConnectionMode(t *testing.T) {
	for _, tc := range []struct {
		name          string
		family, child int
	}{
		{"tcp", 1, 1}, {"tcpmux", 1, 2}, {"stealth", 1, 3},
		{"udp", 2, 1}, {"kcp", 2, 2}, {"quic", 2, 3},
		{"ws", 3, 1}, {"wsmux", 3, 2}, {"wss", 3, 3}, {"wssmux", 3, 4},
		{"xdi", 4, 1}, {"spoof", 4, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := runSetupFlow(t, strings.Join([]string{
				string(rune('0' + tc.family)), string(rune('0' + tc.child)), "0", "",
			}, "\n"))
			if !strings.Contains(out, "Connection mode:") {
				t.Fatalf("mode prompt missing after %s selection:\n%s", tc.name, out)
			}
		})
	}
}

func TestDirectModePromptPrecedesPortQuestions(t *testing.T) {
	out := runSetupFlow(t, "1\n1\n1\nnot-a-port\n\n")
	modeAt := strings.Index(out, "Connection mode:")
	portAt := strings.Index(out, "Tunnel (control) port:")
	if modeAt < 0 || portAt < 0 || modeAt >= portAt {
		t.Fatalf("Direct/Reverse must be asked after transport and before ports:\n%s", out)
	}
	if strings.Contains(out, "Kharej server address") {
		t.Fatal("an invalid tunnel port must stop the novice flow before later questions")
	}
}
