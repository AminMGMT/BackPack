package e2e

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Reported from several Iran servers on v1.8.4: reverse tunnels on an unsteady
// path kept dropping, the log filled with "restarting server...", and users on
// the forwarded ports were cut every time. Each time the kharej re-dialed — and
// on those paths it re-dials often — the Iran side rebuilt the whole run: the
// tunnel port and every forwarded port were closed and bound again, and every
// user connection through them ended.
//
// A generation now outlives its clients (see transport.clientSeat). This runs
// the real binaries: a kharej is killed without a word, as a lost path looks
// from the Iran side, and another takes its place. Throughout, the forwarded
// port must stay open, the Iran side must not restart, and traffic must flow
// again once the new kharej is in.
func TestAKharejReconnectingDoesNotRestartTheIranSide(t *testing.T) {
	bin := currentBinary(t)
	for _, transport := range []string{"tcp", "tcpmux", "ws", "wsmux", "kcp", "quic", "udp"} {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			udp := transport == "udp"
			backend := ""
			if udp {
				backend = startUDPEchoBackend(t)
			} else {
				backend = startEchoBackend(t).addr
			}
			tunnelPort, entryPort := freePort(t), freePort(t)
			token := "adoption-token-0123456789abcdef"
			srvCfg := filepath.Join(dir, "iran.toml")
			writeFile(t, srvCfg, fmt.Sprintf(`[server]
bind_addr = "127.0.0.1:%d"
transport = "%s"
token = "%s"
ports = ["%d=%s"]
heartbeat = 2
log_level = "info"
skip_optz = true
`, tunnelPort, transport, token, entryPort, backend))
			cliCfg := filepath.Join(dir, "kharej.toml")
			writeFile(t, cliCfg, fmt.Sprintf(`[client]
remote_addr = "127.0.0.1:%d"
transport = "%s"
token = "%s"
log_level = "info"
skip_optz = true
`, tunnelPort, transport, token))

			srvLog := filepath.Join(dir, "iran.log")
			startLoggedEngine(t, bin, srvCfg, srvLog)
			time.Sleep(700 * time.Millisecond)
			first := startLoggedEngine(t, bin, cliCfg, filepath.Join(dir, "kharej-1.log"))

			entry := fmt.Sprintf("127.0.0.1:%d", entryPort)
			if err := awaitForwarded(entry, udp, 25*time.Second); err != nil {
				t.Fatalf("the tunnel never carried traffic: %v\n%s", err, tailFile(srvLog))
			}

			// The kharej dies without a word.
			_ = first.Process.Kill()
			_, _ = first.Process.Wait()

			// While there is no kharej, the forwarded port stays where it was.
			if !udp {
				for i := 0; i < 10; i++ {
					c, err := net.DialTimeout("tcp", entry, time.Second)
					if err != nil {
						t.Fatalf("the forwarded port closed while the kharej was away: %v\n%s", err, tailFile(srvLog))
					}
					c.Close()
					time.Sleep(200 * time.Millisecond)
				}
			}

			startLoggedEngine(t, bin, cliCfg, filepath.Join(dir, "kharej-2.log"))
			if err := awaitForwarded(entry, udp, 40*time.Second); err != nil {
				t.Fatalf("the tunnel did not carry traffic after the kharej came back: %v\n%s", err, tailFile(srvLog))
			}

			log, _ := os.ReadFile(srvLog)
			if n := strings.Count(string(log), "restarting server"); n > 0 {
				t.Fatalf("the Iran side restarted %d times for one kharej coming back\n%s", n, tailFile(srvLog))
			}
		})
	}
}

// startLoggedEngine runs one binary with its output in a file, and stops it
// when the test ends.
func startLoggedEngine(t *testing.T, bin, cfg, logPath string) *exec.Cmd {
	t.Helper()
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-c", cfg)
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		t.Fatalf("cannot start %s: %v", bin, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		f.Close()
	})
	return cmd
}

// awaitForwarded waits for the forwarded port to echo, over TCP or UDP.
func awaitForwarded(entry string, udp bool, within time.Duration) error {
	if !udp {
		return awaitEcho(entry, within)
	}
	deadline := time.Now().Add(within)
	var last error
	for time.Now().Before(deadline) {
		if last = udpRoundTrip(entry, []byte("adoption-datagram")); last == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return last
}

// tailFile is the last lines of a log, for a failure message.
func tailFile(path string) string {
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > 25 {
		lines = lines[len(lines)-25:]
	}
	return strings.Join(lines, "\n")
}
