package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/client"
	"github.com/backpack/backpack/internal/server"
)

// Upgrade safety.
//
// An update replaces the binary and restarts the services; it never rewrites a
// tunnel's config. So the new binary has to run configs written by the previous
// version, unchanged, and keep carrying traffic — otherwise updating breaks
// every tunnel on the machine at once.
//
// These tests write configs in the exact shape v1.5.0 produced (no keys added
// since), decode them the way the engine does, and run a real tunnel from them.

// v150ServerConfig is a server config exactly as v1.5.0 wrote it, including the
// socket buffers that release pinned onto TCP sockets.
const v150ServerConfig = `[server]
bind_addr = "127.0.0.1:%d"
transport = "tcp"
preset = "turbo"
token = "%s"
channel_size = 4096
keepalive_period = 75
nodelay = true
heartbeat = 40
log_level = "error"
accept_udp = false
mux_con = 8
mux_version = 2
mux_framesize = 32768
mux_recievebuffer = 4194304
mux_streambuffer = 65536
proxy_protocol = false
max_connections = 0
bandwidth_mbps = 0
sniffer = false
web_port = 0
ports = ["%d=%s"]
so_rcvbuf = 8388608
so_sndbuf = 8388608
skip_optz = true
`

// v150ClientConfig is a client config exactly as v1.5.0 wrote it.
const v150ClientConfig = `[client]
remote_addr = "127.0.0.1:%d"
transport = "tcp"
preset = "turbo"
token = "%s"
connection_pool = 8
aggressive_pool = false
keepalive_period = 75
nodelay = true
load_balance = false
retry_interval = 1
dial_timeout = 5
log_level = "error"
mux_session = 8
mux_version = 2
mux_framesize = 32768
mux_recievebuffer = 4194304
mux_streambuffer = 65536
sniffer = false
web_port = 0
so_rcvbuf = 8388608
so_sndbuf = 8388608
skip_optz = true
`

// decodeFile mirrors what the engine does with a config path.
func decodeFile(t *testing.T, path string) *config.Config {
	t.Helper()
	var cfg config.Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		t.Fatalf("a config written by the previous version failed to decode: %v", err)
	}
	return &cfg
}

// A config from the previous release must still decode, keep every value it
// had, and pick up the new defaults for keys it does not contain.
func TestPreviousVersionConfigStillDecodes(t *testing.T) {
	dir := t.TempDir()
	srvPath := filepath.Join(dir, "srv.toml")
	body := fmt.Sprintf(v150ServerConfig, 1234, "tok-0123456789abcdefghijklmno", 8443, "127.0.0.1:9000")
	if err := os.WriteFile(srvPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := decodeFile(t, srvPath)

	// Values the old config set must survive verbatim.
	if cfg.Server.Token != "tok-0123456789abcdefghijklmno" {
		t.Fatalf("token lost: %q", cfg.Server.Token)
	}
	if cfg.Server.SO_RCVBUF != 8388608 || cfg.Server.SO_SNDBUF != 8388608 {
		t.Fatalf("socket buffers lost: %d/%d", cfg.Server.SO_RCVBUF, cfg.Server.SO_SNDBUF)
	}
	if cfg.Server.MuxCon != 8 || cfg.Server.ChannelSize != 4096 {
		t.Fatalf("mux/channel settings lost: %+v", cfg.Server)
	}
	if len(cfg.Server.Ports) != 1 {
		t.Fatalf("port mapping lost: %v", cfg.Server.Ports)
	}

	// A key that did not exist in the old config must land on the new default.
	// so_pin_tcp absent means false, which is what applies the throughput fix
	// to an upgraded tunnel without the operator editing anything.
	if cfg.Server.SOPinTCP {
		t.Fatal("so_pin_tcp must default to false on a config that predates it")
	}
}

// The decisive one: a tunnel built from the previous version's configs, run by
// this build, must still carry traffic.
func TestPreviousVersionConfigStillCarriesTraffic(t *testing.T) {
	backend := startEchoBackend(t)
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "upgrade-token-0123456789abcdef"

	dir := t.TempDir()
	srvPath := filepath.Join(dir, "srv.toml")
	cliPath := filepath.Join(dir, "cli.toml")
	if err := os.WriteFile(srvPath,
		[]byte(fmt.Sprintf(v150ServerConfig, tunnelPort, token, entryPort, backend.addr)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cliPath,
		[]byte(fmt.Sprintf(v150ClientConfig, tunnelPort, token)), 0600); err != nil {
		t.Fatal(err)
	}

	srvCfg := &decodeFile(t, srvPath).Server
	cliCfg := &decodeFile(t, cliPath).Client

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })

	srv := server.NewServer(srvCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); srv.Start() }()
	time.Sleep(300 * time.Millisecond)
	cli := client.NewClient(cliCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); cli.Start() }()

	entry := fmt.Sprintf("127.0.0.1:%d", entryPort)
	deadline := time.Now().Add(tunnelReadyTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if lastErr = poolRoundTrip(entry, []byte("upgraded-and-still-working")); lastErr == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("a tunnel from the previous version's config stopped carrying traffic: %v", lastErr)
}
