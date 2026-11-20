package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Once a client has proved itself the v2.0.0 way, the server stops accepting
// the older handshake for that tunnel — across a restart too, because the
// proof is remembered beside the config. Without that, anything on the path
// could make the new handshake fail and push the pair back to the one that
// sends the token as it is (docs/adr/0006-plain-transports-prove-both-ends.md).
//
// Needs the previous release as the "older client"; skipped without it, like
// TestThePreviousReleaseStillTalksToThisOne.
func TestAProvedTunnelRefusesTheOlderHandshake(t *testing.T) {
	prev := os.Getenv(prevBinaryEnv)
	if prev == "" {
		t.Skipf("set %s to a build of the previous release to run this", prevBinaryEnv)
	}
	current := currentBinary(t)
	for _, transport := range []string{"tcp", "tcpmux", "ws", "wsmux"} {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			backend := startEchoBackend(t).addr
			tunnelPort, entryPort := freePort(t), freePort(t)
			token := "peer-auth-pin-token-0123456789ab"
			dir := t.TempDir()
			serverCfg := filepath.Join(dir, "srv.toml")
			writeFile(t, serverCfg, fmt.Sprintf(`[server]
bind_addr = "127.0.0.1:%d"
transport = "%s"
token = "%s"
ports = ["%d=%s"]
log_level = "error"
skip_optz = true
`, tunnelPort, transport, token, entryPort, backend))
			clientCfg := filepath.Join(dir, "cli.toml")
			writeFile(t, clientCfg, fmt.Sprintf(`[client]
remote_addr = "127.0.0.1:%d"
transport = "%s"
token = "%s"
log_level = "error"
skip_optz = true
retry_interval = 1
`, tunnelPort, transport, token))
			entry := fmt.Sprintf("127.0.0.1:%d", entryPort)

			server := startEngineCmd(t, current, serverCfg)
			time.Sleep(700 * time.Millisecond)
			client := startEngineCmd(t, current, clientCfg)
			if err := awaitEcho(entry, 25*time.Second); err != nil {
				t.Fatalf("two v2 ends never carried traffic: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, ".srv.peer")); err != nil {
				t.Fatalf("the server did not remember the proved client: %v", err)
			}
			stop(client)

			// Restart the server too: the proof is on disk, not in memory.
			stop(server)
			server = startEngineCmd(t, current, serverCfg)
			time.Sleep(700 * time.Millisecond)
			old := startEngineCmd(t, prev, clientCfg)
			if err := awaitEcho(entry, 6*time.Second); err == nil {
				t.Fatal("a client on the older handshake was served by a server that had seen a v2 client")
			}

			// The control: without the remembered proof the same older client
			// is served, so the refusal above was the proof's doing.
			stop(old)
			stop(server)
			if err := os.Remove(filepath.Join(dir, ".srv.peer")); err != nil {
				t.Fatal(err)
			}
			startEngineCmd(t, current, serverCfg)
			time.Sleep(700 * time.Millisecond)
			startEngineCmd(t, prev, clientCfg)
			if err := awaitEcho(entry, 25*time.Second); err != nil {
				t.Fatalf("with the proof forgotten, the older client was still not served: %v", err)
			}
		})
	}
}

func startEngineCmd(t *testing.T, bin, cfg string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(bin, "-c", cfg)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("cannot start %s: %v", bin, err)
	}
	t.Cleanup(func() { stop(cmd) })
	return cmd
}

func stop(cmd *exec.Cmd) {
	if cmd.ProcessState != nil {
		return
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}
