package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/cmd"
)

// Editing a tunnel's configuration file has to take effect on its own.
//
// This drives the whole path a real edit takes: cmd.Run loads the file, starts
// the tunnel, notices the file changed, stops the tunnel and starts it again
// from the new file — inside one process, with no service manager involved.
// Carrying traffic through the new forwarded port is the assertion, because
// everything short of that can look like it worked while the tunnel still runs
// on what it was originally given.
func TestConfigReloadAppliesANewForwardedPort(t *testing.T) {
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	firstEntry := freePort(t)
	secondEntry := freePort(t)

	dir := scratchDir(t)
	serverPath := filepath.Join(dir, "server.toml")
	clientPath := filepath.Join(dir, "client.toml")

	writeTunnelConfig(t, serverPath, serverToml(tunnelPort, firstEntry, backend.addr))
	writeTunnelConfig(t, clientPath, clientToml(tunnelPort))

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); cmd.Run(serverPath, ctx) }()
	time.Sleep(300 * time.Millisecond)
	go func() { defer wg.Done(); cmd.Run(clientPath, ctx) }()
	t.Cleanup(func() { cancel(); wg.Wait() })

	if err := echoThrough(fmt.Sprintf("127.0.0.1:%d", firstEntry), tunnelReadyTimeout); err != nil {
		t.Fatalf("the tunnel never came up on its original port: %v", err)
	}

	// The edit: forward a different port to the same backend.
	writeTunnelConfig(t, serverPath, serverToml(tunnelPort, secondEntry, backend.addr))

	if err := echoThrough(fmt.Sprintf("127.0.0.1:%d", secondEntry), 45*time.Second); err != nil {
		t.Fatalf("the edited configuration was never applied: %v", err)
	}
}

// A file that stops parsing must leave the tunnel running on what it already
// had. This is the property that makes watching the file safe at all: a typo
// during an ssh session must not become an outage.
func TestConfigReloadKeepsRunningOnABrokenFile(t *testing.T) {
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	entry := freePort(t)

	dir := scratchDir(t)
	serverPath := filepath.Join(dir, "server.toml")
	clientPath := filepath.Join(dir, "client.toml")

	writeTunnelConfig(t, serverPath, serverToml(tunnelPort, entry, backend.addr))
	writeTunnelConfig(t, clientPath, clientToml(tunnelPort))

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); cmd.Run(serverPath, ctx) }()
	time.Sleep(300 * time.Millisecond)
	go func() { defer wg.Done(); cmd.Run(clientPath, ctx) }()
	t.Cleanup(func() { cancel(); wg.Wait() })

	entryAddr := fmt.Sprintf("127.0.0.1:%d", entry)
	if err := echoThrough(entryAddr, tunnelReadyTimeout); err != nil {
		t.Fatalf("the tunnel never came up: %v", err)
	}

	writeTunnelConfig(t, serverPath, "[server\nnot valid toml")

	// Well past a poll interval, so a tunnel that was going to be taken down
	// by this would already be down.
	time.Sleep(6 * time.Second)

	if err := echoThrough(entryAddr, 10*time.Second); err != nil {
		t.Fatalf("a configuration file that does not parse took the tunnel down: %v", err)
	}
}

// scratchDir is a temporary directory the test cleans up best effort, rather
// than t.TempDir().
//
// The tunnel writes its metrics file next to its configuration, from a
// collector goroutine on a thirty-second tick that nothing here can join. That
// goroutine can land after the test has finished, and t.TempDir() fails the
// test when the directory it is removing is not empty — turning correct
// behaviour into a red test. What is being verified is the reload, not the
// tidiness of /tmp.
func scratchDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "backpack-reload-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func serverToml(tunnelPort, entryPort int, backendAddr string) string {
	return fmt.Sprintf(`
[server]
bind_addr = "127.0.0.1:%d"
transport = "tcp"
token = "reload-token-0123456789abcdef"
ports = ["%d=%s"]
log_level = "error"
`, tunnelPort, entryPort, backendAddr)
}

func clientToml(tunnelPort int) string {
	return fmt.Sprintf(`
[client]
remote_addr = "127.0.0.1:%d"
transport = "tcp"
token = "reload-token-0123456789abcdef"
connection_pool = 4
log_level = "error"
`, tunnelPort)
}

func writeTunnelConfig(t *testing.T, path, body string) {
	t.Helper()
	// Written to a temporary file and renamed into place, because the tunnel is
	// watching this path while the test rewrites it. os.WriteFile truncates and
	// then writes, so a watcher that looks in between sees a file that is empty
	// or half a table — which the engine correctly refuses to parse and says so,
	// loudly, in a log somebody then has to explain. Rename is atomic on POSIX:
	// the watcher sees either the old file or the new one.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename %s -> %s: %v", tmp, path, err)
	}
	// The watcher identifies a version of the file by size and modification
	// time; a coarse filesystem clock can report the same pair for two writes
	// in quick succession, which a real edit minutes later would never hit.
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// echoThrough keeps trying until the entry address carries a round trip, or the
// timeout runs out.
func echoThrough(entry string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = oneEcho(entry)
		if lastErr == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return lastErr
}

func oneEcho(entry string) error {
	c, err := net.DialTimeout("tcp", entry, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}

	payload := []byte("reload-check")
	if _, err := c.Write(payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err := readFull(c, got); err != nil {
		return err
	}
	if string(got) != string(payload) {
		return fmt.Errorf("echoed %q, want %q", got, payload)
	}
	return nil
}

func readFull(c net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := c.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
