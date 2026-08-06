package cmd

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/sirupsen/logrus"
)

func TestMain(m *testing.M) {
	// These tests deliberately feed the loader broken files, and the complaints
	// belong in the code's own output, not in the test log.
	logger.SetOutput(os.NewFile(0, os.DevNull))
	logger.SetLevel(logrus.FatalLevel)
	os.Exit(m.Run())
}

func writeConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	// Size and modification time identify a version of the file, and a
	// filesystem whose timestamps are coarse can hand back the same pair for
	// two writes in the same instant. Stepping the time forward makes the edit
	// unambiguous, which is what an edit minutes later would be anyway.
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func loadFixture(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	applyDefaults(cfg)
	return cfg
}

func serverConfig(port string) string {
	return "\n[server]\nbind_addr = \"0.0.0.0:" + port + "\"\ntransport = \"tcp\"\ntoken = \"a-token\"\nports = [\"8080\"]\n"
}

// A real edit is picked up and handed back.
func TestAwaitConfigChangeReturnsAChangedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tunnel.toml")
	writeConfig(t, path, serverConfig("3080"))
	current := loadFixture(t, path)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	go func() {
		time.Sleep(configPollInterval / 2)
		writeConfig(t, path, serverConfig("3090"))
	}()

	next := awaitConfigChange(ctx, path, current)
	if next == nil {
		t.Fatal("an edited configuration was never reported")
	}
	if next.Server.BindAddr != "0.0.0.0:3090" {
		t.Fatalf("reported bind_addr %q, want the edited one", next.Server.BindAddr)
	}
}

// A file that no longer parses must leave the running tunnel alone. A
// half-saved file or a typo turning into an outage would be strictly worse than
// the manual restart this replaces.
func TestAwaitConfigChangeIgnoresAnUnparseableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tunnel.toml")
	writeConfig(t, path, serverConfig("3080"))
	current := loadFixture(t, path)

	ctx, cancel := context.WithTimeout(context.Background(), 3*configPollInterval)
	defer cancel()

	go func() {
		time.Sleep(configPollInterval / 2)
		writeConfig(t, path, "[server\nthis is not toml at all")
	}()

	if next := awaitConfigChange(ctx, path, current); next != nil {
		t.Fatalf("a file that does not parse was applied: %+v", next.Server)
	}
}

// Rewriting the file with the same meaning must not restart the tunnel. Every
// connection it is carrying would be dropped for nothing.
func TestAwaitConfigChangeIgnoresAMeaninglessRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tunnel.toml")
	body := serverConfig("3080")
	writeConfig(t, path, body)
	current := loadFixture(t, path)

	ctx, cancel := context.WithTimeout(context.Background(), 3*configPollInterval)
	defer cancel()

	go func() {
		time.Sleep(configPollInterval / 2)
		// Same settings, different bytes: a new comment and reordered
		// whitespace, which is what an editor round-trip tends to produce.
		writeConfig(t, path, "# edited by hand\n"+body+"\n")
	}()

	if next := awaitConfigChange(ctx, path, current); next != nil {
		t.Fatal("a rewrite that changed nothing restarted the tunnel")
	}
}

// A change that only differs in a defaulted field must also be ignored: the
// comparison happens after defaults are applied, so writing out the value that
// was already implied is not a change.
func TestAwaitConfigChangeIgnoresAnExplicitDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tunnel.toml")
	writeConfig(t, path, serverConfig("3080"))
	current := loadFixture(t, path)

	ctx, cancel := context.WithTimeout(context.Background(), 3*configPollInterval)
	defer cancel()

	go func() {
		time.Sleep(configPollInterval / 2)
		writeConfig(t, path, serverConfig("3080")+"\nchannel_size = 2048\n")
	}()

	if next := awaitConfigChange(ctx, path, current); next != nil {
		t.Fatalf("writing out an already-default value restarted the tunnel: %+v", next.Server)
	}
}

// Shutting down must win over waiting for an edit, and must be reported as
// "stop" rather than as a reload.
func TestAwaitConfigChangeStopsWithTheContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tunnel.toml")
	writeConfig(t, path, serverConfig("3080"))
	current := loadFixture(t, path)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if next := awaitConfigChange(ctx, path, current); next != nil {
		t.Fatal("a cancelled context was reported as a configuration change")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %v to notice the shutdown", elapsed)
	}
}

// A file that disappears — mid-rename, say — must not be mistaken for a change,
// and must not stop the watcher looking.
func TestAwaitConfigChangeSurvivesAMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tunnel.toml")
	writeConfig(t, path, serverConfig("3080"))
	current := loadFixture(t, path)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	go func() {
		time.Sleep(configPollInterval / 2)
		os.Remove(path)
		time.Sleep(configPollInterval)
		writeConfig(t, path, serverConfig("3090"))
	}()

	next := awaitConfigChange(ctx, path, current)
	if next == nil {
		t.Fatal("the watcher gave up when the file briefly went missing")
	}
	if next.Server.BindAddr != "0.0.0.0:3090" {
		t.Fatalf("reported bind_addr %q, want the edited one", next.Server.BindAddr)
	}
}

func TestPortsInUse(t *testing.T) {
	server := &config.Config{}
	server.Server.BindAddr = "0.0.0.0:3080"
	server.Server.WebPort = 2060
	got := portsInUse(server)
	if len(got) != 2 || got[0] != (listenerBinding{network: "tcp", address: "0.0.0.0:3080"}) || got[1] != (listenerBinding{network: "tcp", address: ":2060"}) {
		t.Fatalf("server ports = %v, want the bind address and the web port", got)
	}

	client := &config.Config{}
	client.Client.RemoteAddr = "example.com:3080"
	client.Client.WebPort = 2061
	got = portsInUse(client)
	if len(got) != 1 || got[0] != (listenerBinding{network: "tcp", address: ":2061"}) {
		t.Fatalf("client ports = %v, want only the web port", got)
	}

	// A client with no panel binds nothing that can be named here.
	bare := &config.Config{}
	bare.Client.RemoteAddr = "example.com:3080"
	if got := portsInUse(bare); len(got) != 0 {
		t.Fatalf("a client with no web port reported %v", got)
	}
}

func TestTunnelNetwork(t *testing.T) {
	tests := []struct {
		transport config.TransportType
		want      string
	}{
		{config.TCP, "tcp"},
		{config.TCPMUX, "tcp"},
		{config.WS, "tcp"},
		{config.WSMUX, "tcp"},
		{config.UDP, "udp"},
		{config.KCP, "udp"},
		{config.QUIC, "udp"},
		{config.XDI, ""},
		{config.SPOOF, ""},
	}
	for _, tt := range tests {
		if got := tunnelNetwork(tt.transport); got != tt.want {
			t.Errorf("tunnelNetwork(%q) = %q, want %q", tt.transport, got, tt.want)
		}
	}
}

// waitForPorts must return once the address is free, and must give up rather
// than hang when it never comes free.
func TestWaitForPorts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	// Held open: this cannot succeed, so it has to give up on the timeout.
	start := time.Now()
	waitForPorts(context.Background(), []listenerBinding{{network: "tcp", address: addr}})
	held := time.Since(start)
	ln.Close()
	if held < portSettleTimeout {
		t.Fatalf("gave up after %v without waiting out the settle timeout", held)
	}

	// Now free: this should return almost immediately.
	start = time.Now()
	waitForPorts(context.Background(), []listenerBinding{{network: "tcp", address: addr}})
	if free := time.Since(start); free > 2*time.Second {
		t.Fatalf("took %v to notice a free port", free)
	}
}

func TestWaitForUDPPort(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen UDP: %v", err)
	}
	binding := listenerBinding{network: "udp", address: pc.LocalAddr().String()}

	go func() {
		time.Sleep(200 * time.Millisecond)
		pc.Close()
	}()
	start := time.Now()
	waitForPorts(context.Background(), []listenerBinding{binding})
	if waited := time.Since(start); waited < 150*time.Millisecond || waited > 2*time.Second {
		t.Fatalf("waited %v for a UDP listener released after 200ms", waited)
	}
}
