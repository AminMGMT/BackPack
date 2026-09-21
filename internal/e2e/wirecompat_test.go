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

// Does the previous release still talk to this one?
//
// The config half of this question is already covered next door in
// upgrade_test.go: a config written by an older version is decoded and run by
// the current binary. That is the right test for "an update never rewrites a
// tunnel's config", and it says nothing at all about the wire.
//
// The wire is where the risk actually is, and the codebase is full of careful
// handling that nothing verifies. The layer-3 handshake treats an empty payload
// as a peer built before that field existed. The mux version is settled on the
// control channel rather than pinned, with a paragraph explaining that raising
// it would otherwise break every mux tunnel the moment one end was upgraded.
// Every one of those accommodations is an assertion in a comment.
//
// It matters more than usual here because of how an update lands: the two ends
// of a tunnel are two machines, and they are never updated at the same instant.
// There is always a window — minutes if the operator is attentive, weeks if
// they are not — in which one end is new and the other is not. A wire change
// that forgets this does not degrade; it takes the tunnel down until somebody
// notices and updates the other side, over a link that may be the only way to
// reach it.
//
// So: both ends built from real binaries, one of them the previous release, in
// both directions.
//
// Skipped unless BACKPACK_PREV_BINARY points at a build of the previous
// release. The CI job that sets it builds that from the last tag; there is
// nothing to check out or fetch here, so an ordinary `go test ./...` on a
// laptop skips this rather than failing on a missing artefact.

const (
	prevBinaryEnv = "BACKPACK_PREV_BINARY"
	// currentBinaryEnv lets the CI job hand over a binary it has already built
	// rather than having this build one. Empty means build it here.
	currentBinaryEnv = "BACKPACK_CURRENT_BINARY"
)

func TestThePreviousReleaseStillTalksToThisOne(t *testing.T) {
	prev := os.Getenv(prevBinaryEnv)
	if prev == "" {
		t.Skipf("set %s to a build of the previous release to run this", prevBinaryEnv)
	}
	if _, err := os.Stat(prev); err != nil {
		t.Fatalf("%s=%q is not a file: %v", prevBinaryEnv, prev, err)
	}
	current := currentBinary(t)

	// Both directions, because a wire change can break either end's reading of
	// the other. An old server with a new client is the shape an operator gets
	// when they update the machine they can reach first; the reverse is what
	// they get when they update the other one.
	for _, tc := range []struct {
		name           string
		server, client string
	}{
		{"old server, new client", prev, current},
		{"new server, old client", current, prev},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runCrossVersionPair(t, tc.server, tc.client)
		})
	}
}

// runCrossVersionPair starts one end from each binary and moves bytes through
// the forwarded port.
func runCrossVersionPair(t *testing.T, serverBin, clientBin string) {
	t.Helper()

	backend := startEchoBackend(t)
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "wire-compat-token-0123456789abcd"
	dir := t.TempDir()

	serverCfg := filepath.Join(dir, "srv.toml")
	writeFile(t, serverCfg, fmt.Sprintf(`[server]
bind_addr = "127.0.0.1:%d"
transport = "tcp"
token = "%s"
ports = ["%d=%s"]
log_level = "error"
skip_optz = true
`, tunnelPort, token, entryPort, backend.addr))

	clientCfg := filepath.Join(dir, "cli.toml")
	writeFile(t, clientCfg, fmt.Sprintf(`[client]
remote_addr = "127.0.0.1:%d"
transport = "tcp"
token = "%s"
log_level = "error"
skip_optz = true
`, tunnelPort, token))

	startEngine(t, serverBin, serverCfg)
	// The server binds before the client dials; without this the client's first
	// attempt fails and it waits out its retry interval, which is longer than
	// this test's patience for no reason that is about compatibility.
	time.Sleep(700 * time.Millisecond)
	startEngine(t, clientBin, clientCfg)

	entry := net.JoinHostPort("127.0.0.1", fmt.Sprint(entryPort))
	if err := awaitEcho(entry, 25*time.Second); err != nil {
		t.Fatalf("a tunnel between these two versions never carried traffic: %v\n"+
			"server %s\nclient %s", err, serverBin, clientBin)
	}
}

// startEngine runs one binary in engine mode and stops it when the test ends.
func startEngine(t *testing.T, bin, cfg string) {
	t.Helper()
	cmd := exec.Command(bin, "-c", cfg)
	// Inherited so a failure is visible in the test log rather than swallowed.
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("cannot start %s: %v", bin, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
}

// awaitEcho keeps trying the forwarded port until it echoes, or gives up.
func awaitEcho(addr string, within time.Duration) error {
	payload := []byte("cross-version round trip")
	deadline := time.Now().Add(within)
	var last error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			last = err
			time.Sleep(300 * time.Millisecond)
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err = conn.Write(payload); err == nil {
			got := make([]byte, len(payload))
			if _, err = readFull(conn, got); err == nil && string(got) == string(payload) {
				conn.Close()
				return nil
			}
		}
		last = err
		conn.Close()
		time.Sleep(300 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("no round trip within %s", within)
	}
	return last
}

// currentBinary returns a build of the tree as it stands.
func currentBinary(t *testing.T) string {
	t.Helper()
	if b := os.Getenv(currentBinaryEnv); b != "" {
		return b
	}
	out := filepath.Join(t.TempDir(), "backpack-current")
	cmd := exec.Command("go", "build", "-o", out, "../..")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("cannot build the current tree: %v", err)
	}
	return out
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("cannot write %s: %v", path, err)
	}
}
