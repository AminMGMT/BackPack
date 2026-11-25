package e2e

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A tunnel whose two ends are updated one at a time.
//
// The six plain transports changed their wire after v2.0.0, so an Iran server
// updated before its kharej serves that kharej on v2.0.0's engine and remembers
// so beside its config (.<name>.compat). The risk in that is a pair that never
// leaves: both ends updated, and the remembered engine keeping them on the old
// one. TestThePreviousReleaseStillTalksToThisOne covers the mixed pairs; these
// cover the way out of them.

var plainTransports = []string{"tcp", "tcpmux", "ws", "wss", "wsmux", "wssmux"}

// An updated pair whose Iran server still remembers a v2.0.0 kharej ends up on
// the plain engine, and the Iran server forgets the old one.
func TestAnUpdatedPairLeavesTheV2Engine(t *testing.T) {
	bin := currentBinary(t)
	for _, transport := range plainTransports {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			pin := filepath.Join(dir, ".srv.compat")
			writeFile(t, pin, "v2\n")
			entry := startCompatPair(t, dir, bin, bin, transport)
			if err := awaitEcho(entry, 30*time.Second); err != nil {
				t.Fatalf("an updated pair that started on the v2.0.0 engine never carried traffic: %v", err)
			}
			if waitFor(20*time.Second, func() bool { _, err := os.Stat(pin); return os.IsNotExist(err) }) != nil {
				t.Fatal("both ends are updated, yet the Iran server still remembers a v2.0.0 kharej")
			}
			if err := awaitEcho(entry, 30*time.Second); err != nil {
				t.Fatalf("the tunnel did not carry traffic once back on the plain engine: %v", err)
			}
		})
	}
}

// An updated Iran server with a v2.0.0 kharej serves it, and remembers that it
// did, so a restart does not try the plain engine first.
func TestAnUpdatedIranServerRemembersAV2Kharej(t *testing.T) {
	prev := os.Getenv(prevBinaryEnv)
	if prev == "" {
		t.Skipf("set %s to a build of v2.0.0 to run this", prevBinaryEnv)
	}
	bin := currentBinary(t)
	for _, transport := range plainTransports {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			pin := filepath.Join(dir, ".srv.compat")
			entry := startCompatPair(t, dir, bin, prev, transport)
			if err := awaitEcho(entry, 30*time.Second); err != nil {
				t.Fatalf("an updated Iran server never carried a v2.0.0 kharej's traffic: %v", err)
			}
			if waitFor(10*time.Second, func() bool {
				b, err := os.ReadFile(pin)
				return err == nil && strings.TrimSpace(string(b)) == "v2"
			}) != nil {
				t.Fatal("the Iran server serves a v2.0.0 kharej but did not remember it")
			}
		})
	}
}

// startCompatPair runs a server (srv.toml) and a client (cli.toml) in dir and
// returns the forwarded port.
func startCompatPair(t *testing.T, dir, serverBin, clientBin, transport string) string {
	t.Helper()
	backend := startEchoBackend(t).addr
	tunnelPort, entryPort := freePort(t), freePort(t)
	token := "compat-token-0123456789abcdef012"
	srv := filepath.Join(dir, "srv.toml")
	writeFile(t, srv, fmt.Sprintf(`[server]
bind_addr = "127.0.0.1:%d"
transport = "%s"
token = "%s"
ports = ["%d=%s"]
log_level = "error"
skip_optz = true
`, tunnelPort, transport, token, entryPort, backend))
	cli := filepath.Join(dir, "cli.toml")
	writeFile(t, cli, fmt.Sprintf(`[client]
remote_addr = "127.0.0.1:%d"
transport = "%s"
token = "%s"
log_level = "error"
skip_optz = true
`, tunnelPort, transport, token))
	startEngine(t, serverBin, srv)
	time.Sleep(700 * time.Millisecond)
	startEngine(t, clientBin, cli)
	return net.JoinHostPort("127.0.0.1", fmt.Sprint(entryPort))
}
