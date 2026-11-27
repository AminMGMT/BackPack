package e2e

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A forwarded port another program already holds is that port's problem, not
// the tunnel's. The plain engine used to end the process on it, and systemd
// brought it back to fail the same way: every other port of the tunnel went
// down with the busy one. Now the busy port is tried again until it is free,
// and the others carry traffic all along.
func TestABusyForwardedPortLeavesTheOthersUp(t *testing.T) {
	bin := currentBinary(t)
	for _, tr := range []string{"tcp", "tcpmux", "ws", "wsmux"} {
		t.Run(tr, func(t *testing.T) {
			t.Parallel()
			backend := startEchoBackend(t).addr
			busyPort, freeEntry, tunnelPort := freePort(t), freePort(t), freePort(t)
			// Another program on the port the tunnel wants.
			squatter, err := net.Listen("tcp", fmt.Sprintf(":%d", busyPort))
			if err != nil {
				t.Fatal(err)
			}
			defer squatter.Close()

			dir := t.TempDir()
			token := "busy-port-token-0123456789abcdef"
			srv := filepath.Join(dir, "srv.toml")
			writeFile(t, srv, fmt.Sprintf(`[server]
bind_addr = "127.0.0.1:%d"
transport = "%s"
token = "%s"
ports = ["%d=%s", "%d=%s"]
log_level = "error"
skip_optz = true
`, tunnelPort, tr, token, busyPort, backend, freeEntry, backend))
			cli := filepath.Join(dir, "cli.toml")
			writeFile(t, cli, fmt.Sprintf(`[client]
remote_addr = "127.0.0.1:%d"
transport = "%s"
token = "%s"
log_level = "error"
skip_optz = true
retry_interval = 1
`, tunnelPort, tr, token))

			server := exec.Command(bin, "-c", srv)
			server.Stdout, server.Stderr = os.Stderr, os.Stderr
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- server.Wait() }()
			t.Cleanup(func() {
				_ = server.Process.Kill()
				<-exited
			})
			time.Sleep(700 * time.Millisecond)
			startEngine(t, bin, cli)

			if err := awaitEcho(fmt.Sprintf("127.0.0.1:%d", freeEntry), 25*time.Second); err != nil {
				t.Fatalf("the free port never carried traffic while the other was busy: %v", err)
			}
			select {
			case err := <-exited:
				t.Fatalf("the Iran server exited over one busy port: %v", err)
			default:
			}

			// The other program lets go; the tunnel takes the port.
			squatter.Close()
			if err := awaitEcho(fmt.Sprintf("127.0.0.1:%d", busyPort), 20*time.Second); err != nil {
				t.Fatalf("the port was never taken up once it was free: %v", err)
			}
			if err := awaitEcho(fmt.Sprintf("127.0.0.1:%d", freeEntry), 5*time.Second); err != nil {
				t.Fatalf("the other port stopped carrying traffic: %v", err)
			}
		})
	}
}
