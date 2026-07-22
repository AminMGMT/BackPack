// Package e2e runs the tunnel end to end: a real server and a real client, in
// this process, carrying real traffic to a real backend.
//
// These tests exist because every bug found in this project so far — a health
// check that probed the wrong protocol, a port check that looked at TCP for a
// UDP tunnel — was found by hand on a live server, after release. Anything the
// tunnel is supposed to do is asserted here instead.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/client"
	"github.com/backpack/backpack/internal/server"
)

// tunnelReadyTimeout is how long a tunnel gets to come up before a test fails.
// Establishing a control channel plus a connection pool takes a moment even on
// loopback; being generous here costs nothing when things work.
const tunnelReadyTimeout = 20 * time.Second

// Port allocation.
//
// Asking the kernel for a free port and immediately releasing it is racy: two
// parallel tests can be handed the same one, and the tunnel binds it seconds
// later. A failure to bind is fatal in the engine, which takes the whole test
// binary down with it — so ports are handed out from a private range instead,
// never reused within a run, and checked free on both TCP and UDP first.
var (
	portMu   sync.Mutex
	nextPort = 34000 + (int(time.Now().UnixNano()/1e6) % 8000)
	issued   = map[int]bool{}
)

func freePort(t *testing.T) int {
	t.Helper()
	portMu.Lock()
	defer portMu.Unlock()

	for attempts := 0; attempts < 4000; attempts++ {
		port := nextPort
		nextPort++
		if nextPort > 60000 {
			nextPort = 34000
		}
		if issued[port] || !portFree(port) {
			continue
		}
		issued[port] = true
		return port
	}
	t.Fatal("no free port available for the test")
	return 0
}

// waitPortFree blocks until nothing is bound to a port, so a test that
// deliberately restarts a server does not race its predecessor's listener.
func waitPortFree(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if portFree(port) {
			// Both servers live in this one process, so the old socket is
			// released by the runtime rather than by a process exiting. Give
			// that a moment to finish before handing the port on, or the
			// replacement's bind can still lose the race.
			time.Sleep(1500 * time.Millisecond)
			if portFree(port) {
				return
			}
			continue
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("port %d was still bound after %s", port, timeout)
}

// portFree reports whether a port can be bound on both TCP and UDP. Both are
// checked because a tunnel may want either, and a port taken on one protocol
// often means something is about to take the other.
func portFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	l.Close()
	pc, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	pc.Close()
	return true
}

// echoBackend is the service the tunnel forwards to: it echoes whatever it is
// sent, which makes any corruption or truncation visible.
type echoBackend struct {
	addr     string
	listener net.Listener
}

func startEchoBackend(t *testing.T) *echoBackend {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot start the echo backend: %v", err)
	}
	b := &echoBackend{addr: l.Addr().String(), listener: l}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return // listener closed
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()
	t.Cleanup(func() { l.Close() })
	return b
}

// tunnel is a running server/client pair plus the port users connect to.
type tunnel struct {
	// Entry is the address on the "Iran" side that traffic is sent to.
	Entry string
	// TunnelPort is the control-channel port the client dials.
	TunnelPort int

	cancel context.CancelFunc
	wg     *sync.WaitGroup
}

// tunnelOptions tweaks a tunnel for a specific test.
type tunnelOptions struct {
	// ClientRemote overrides the address the client dials. Empty means the
	// server's own address, which is what an ordinary tunnel uses.
	ClientRemote string
	// FallbackAddrs are extra server addresses the client may fail over to.
	FallbackAddrs []string
	// Token overrides the shared secret on both sides.
	Token string
	// ProxyProtocol prepends a PROXY protocol v2 header to each forwarded
	// connection, carrying the real client's address to the backend.
	ProxyProtocol bool
}

// startTunnel brings up a server and a client on the given transport, forwarding
// an entry port to the backend, and waits until traffic actually flows.
func startTunnel(t *testing.T, transport string, backend *echoBackend, opts tunnelOptions) *tunnel {
	t.Helper()

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := opts.Token
	if token == "" {
		token = "e2e-test-token-0123456789abcdef"
	}

	srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)
	srvCfg.ProxyProtocol = opts.ProxyProtocol
	remote := opts.ClientRemote
	if remote == "" {
		remote = fmt.Sprintf("127.0.0.1:%d", tunnelPort)
	}
	cliCfg := baseClientConfig(transport, remote, token, opts.FallbackAddrs)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	srv := server.NewServer(srvCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); srv.Start() }()

	// The client is started after the server so its first dial has something to
	// reach; the client retries anyway, this just keeps the logs quiet.
	time.Sleep(300 * time.Millisecond)

	cli := client.NewClient(cliCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); cli.Start() }()

	tun := &tunnel{
		Entry:      fmt.Sprintf("127.0.0.1:%d", entryPort),
		TunnelPort: tunnelPort,
		cancel:     cancel,
		wg:         &wg,
	}
	t.Cleanup(tun.Stop)

	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("%s tunnel never carried traffic: %v", transport, err)
	}
	return tun
}

// startTunnelVia brings up a tunnel whose client dials clientRemote instead of
// the server directly, so a relay can be placed in the middle of the path.
func startTunnelVia(t *testing.T, transport string, backend *echoBackend,
	tunnelPort, entryPort int, token, clientRemote string) *tunnel {
	t.Helper()

	srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)
	cliCfg := baseClientConfig(transport, clientRemote, token, nil)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	srv := server.NewServer(srvCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); srv.Start() }()

	time.Sleep(300 * time.Millisecond)

	cli := client.NewClient(cliCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); cli.Start() }()

	tun := &tunnel{
		Entry:      fmt.Sprintf("127.0.0.1:%d", entryPort),
		TunnelPort: tunnelPort,
		cancel:     cancel,
		wg:         &wg,
	}
	t.Cleanup(tun.Stop)

	// A degraded path needs longer to establish than a clean one.
	if err := tun.waitReady(45 * time.Second); err != nil {
		t.Fatalf("%s tunnel never carried traffic through the relay: %v", transport, err)
	}
	return tun
}

// runPair starts a server and a client from explicit configs. It exists for
// tests that need to control the addresses each side uses.
func runPair(t *testing.T, srvCfg *config.ServerConfig, cliCfg *config.ClientConfig,
	entryPort, tunnelPort int) *tunnel {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	srv := server.NewServer(srvCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); srv.Start() }()

	time.Sleep(300 * time.Millisecond)

	cli := client.NewClient(cliCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); cli.Start() }()

	tun := &tunnel{
		Entry:      fmt.Sprintf("127.0.0.1:%d", entryPort),
		TunnelPort: tunnelPort,
		cancel:     cancel,
		wg:         &wg,
	}
	t.Cleanup(tun.Stop)
	return tun
}

// startClientAgainst runs only the client half, against a server the caller
// manages, so a test can stop and restart that server underneath it.
func startClientAgainst(t *testing.T, ctx context.Context, cliCfg *config.ClientConfig,
	entryPort, tunnelPort int) *tunnel {
	t.Helper()

	var wg sync.WaitGroup
	cli := client.NewClient(cliCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); cli.Start() }()

	return &tunnel{
		Entry:      fmt.Sprintf("127.0.0.1:%d", entryPort),
		TunnelPort: tunnelPort,
		cancel:     func() {}, // the caller owns the context
		wg:         &wg,
	}
}

// startClientOnly runs a client with no server of its own, used to point an
// extra or misconfigured peer at an existing server.
func startClientOnly(t *testing.T, transport, remote, token string) *tunnel {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	cli := client.NewClient(baseClientConfig(transport, remote, token, nil), ctx)
	wg.Add(1)
	go func() { defer wg.Done(); cli.Start() }()

	return &tunnel{cancel: cancel, wg: &wg}
}

// Stop tears the tunnel down and waits for its goroutines to finish.
func (tn *tunnel) Stop() {
	tn.cancel()
	done := make(chan struct{})
	go func() { tn.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// A transport that will not shut down is a problem, but failing the
		// cleanup would mask whatever the test actually found.
	}
}

// waitReady polls the entry port until a round trip through the tunnel works.
// It checks real data flow rather than a successful connect: the entry port
// accepts connections as soon as it is bound, long before the tunnel behind it
// can carry anything.
func (tn *tunnel) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := tn.roundTrip([]byte("ready?")); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return lastErr
}

// roundTrip sends payload through the tunnel and verifies it comes back byte
// for byte.
func (tn *tunnel) roundTrip(payload []byte) error {
	conn, err := net.DialTimeout("tcp", tn.Entry, 3*time.Second)
	if err != nil {
		return fmt.Errorf("dial entry port: %w", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return err
	}

	writeErr := make(chan error, 1)
	go func() {
		_, err := conn.Write(payload)
		writeErr <- err
	}()

	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		return fmt.Errorf("read back: %w", err)
	}
	if err := <-writeErr; err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if !bytes.Equal(payload, got) {
		return fmt.Errorf("payload came back corrupted (%d bytes)", len(payload))
	}
	return nil
}

// randomPayload returns n random bytes, so a test cannot pass on a stream of
// zeroes that happened to look right.
func randomPayload(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("cannot generate a payload: %v", err)
	}
	return b
}

// baseServerConfig builds a server config equivalent to what the CLI writes for
// the Turbo preset, with the KCP knobs filled so the KCP transport is valid.
func baseServerConfig(transport string, tunnelPort, entryPort int, backendAddr, token string) *config.ServerConfig {
	return &config.ServerConfig{
		BindAddr:         fmt.Sprintf("127.0.0.1:%d", tunnelPort),
		Transport:        config.TransportType(transport),
		Token:            token,
		Nodelay:          true,
		Keepalive:        75,
		ChannelSize:      2048,
		LogLevel:         "error", // keep test output readable
		Ports:            []string{fmt.Sprintf("%d=%s", entryPort, backendAddr)},
		Heartbeat:        20,
		MuxCon:           8,
		MuxSession:       8,
		MuxVersion:       2,
		MaxFrameSize:     32768,
		MaxReceiveBuffer: 4194304,
		MaxStreamBuffer:  65536,
		SO_RCVBUF:        4 << 20,
		SO_SNDBUF:        4 << 20,
		// The engine's own sysctl tuning needs root and is irrelevant here.
		SkipOptz:  true,
		KCPConfig: turboKCP(),
	}
}

func baseClientConfig(transport, remote, token string, fallbacks []string) *config.ClientConfig {
	return &config.ClientConfig{
		RemoteAddr:     remote,
		FallbackAddrs:  fallbacks,
		Transport:      config.TransportType(transport),
		Token:          token,
		ConnectionPool: 4,
		RetryInterval:  1,
		Nodelay:        true,
		// Deliberately short: on KCP this doubles as how long the client waits
		// for word from the server before declaring the tunnel dead, and the
		// recovery tests would otherwise take over a minute each.
		Keepalive:        8,
		LogLevel:         "error",
		MuxSession:       8,
		MuxVersion:       2,
		MaxFrameSize:     32768,
		MaxReceiveBuffer: 4194304,
		MaxStreamBuffer:  65536,
		DialTimeout:      5,
		SO_RCVBUF:        4 << 20,
		SO_SNDBUF:        4 << 20,
		SkipOptz:         true,
		KCPConfig:        turboKCP(),
	}
}

// turboKCP mirrors the Turbo preset's KCP settings. Both ends must agree on
// these — the FEC layer in particular is not negotiated.
func turboKCP() config.KCPConfig {
	return config.KCPConfig{
		MTU:          1350,
		Interval:     20,
		Resend:       2,
		NoDelay:      1,
		NoCongestion: 1,
		SndWnd:       1024,
		RcvWnd:       1024,
		AckNoDelay:   true,
		DataShards:   10,
		ParityShards: 3,
	}
}
