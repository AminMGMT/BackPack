package e2e

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/client"
	"github.com/backpack/backpack/internal/server"
)

// TestForwardTCP proves the corrected direct semantics end to end: the Iran
// process is the dialler and owns the user-facing port, while the listening
// Kharej process owns and dials the backend.
func TestForwardStreamTransports(t *testing.T) {
	for _, transport := range []string{"tcp", "stealth", "tcpmux", "kcp", "quic", "ws", "wss", "wsmux", "wssmux"} {
		t.Run(transport, func(t *testing.T) { testForwardStreamTransport(t, transport) })
	}
}

// A browser, game gateway or proxy can open hundreds of connections at once.
// Direct TCP/Stealth must queue the expensive outer handshakes behind the
// configured pool instead of turning that burst into an outbound dial storm.
func TestForwardTCPBurst(t *testing.T) {
	for _, transport := range []string{"tcp", "stealth"} {
		t.Run(transport, func(t *testing.T) {
			backend := startEchoBackend(t)
			tunnelPort, entryPort := freePort(t), freePort(t)
			token := "forward-burst-token-0123456789abcdef"
			origin := baseServerConfig(transport, tunnelPort, 1, backend.addr, token)
			origin.Ports = nil
			edge := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
			edge.Ports = []string{fmt.Sprintf("%d=%s", entryPort, backend.addr)}
			edge.ConnectionPool = 4

			ctx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			srv := server.NewForwardOrigin(origin, ctx)
			wg.Add(1)
			go func() { defer wg.Done(); srv.Start() }()
			time.Sleep(300 * time.Millisecond)
			cli := client.NewForwardEdge(edge, ctx)
			wg.Add(1)
			go func() { defer wg.Done(); cli.Start() }()
			tun := &tunnel{Entry: fmt.Sprintf("127.0.0.1:%d", entryPort), TunnelPort: tunnelPort, cancel: cancel, wg: &wg}
			t.Cleanup(tun.Stop)
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("forward tunnel never became ready: %v", err)
			}

			payload := randomPayload(t, 128*1024)
			const users = 64
			var failures atomic.Int32
			var burst sync.WaitGroup
			burst.Add(users)
			for range users {
				go func() {
					defer burst.Done()
					if err := tun.roundTrip(payload); err != nil {
						failures.Add(1)
					}
				}()
			}
			burst.Wait()
			if got := failures.Load(); got != 0 {
				t.Fatalf("%d of %d simultaneous forward users failed", got, users)
			}
		})
	}
}

func TestLargePayloadAcrossTCPDirections(t *testing.T) {
	for _, transport := range []string{"tcp", "stealth"} {
		t.Run("direct/"+transport, func(t *testing.T) {
			backend := startEchoBackend(t)
			tunnelPort, entryPort := freePort(t), freePort(t)
			token := "forward-large-token-0123456789abcdef"
			origin := baseServerConfig(transport, tunnelPort, 1, backend.addr, token)
			origin.Ports = nil
			edge := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
			edge.Ports = []string{fmt.Sprintf("%d=%s", entryPort, backend.addr)}
			tun := runForwardPair(t, origin, edge, entryPort, tunnelPort)
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatal(err)
			}
			if err := tun.roundTrip(randomPayload(t, 16*1024*1024)); err != nil {
				t.Fatalf("large direct payload: %v", err)
			}
		})

		t.Run("reverse/"+transport, func(t *testing.T) {
			backend := startEchoBackend(t)
			tunnelPort, entryPort := freePort(t), freePort(t)
			token := "reverse-large-token-0123456789abcdef"
			srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)
			cliCfg := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
			tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatal(err)
			}
			if err := tun.roundTrip(randomPayload(t, 16*1024*1024)); err != nil {
				t.Fatalf("large reverse payload: %v", err)
			}
		})
	}
}

// Reconnecting the control channel creates a new client generation. The old
// generation must release the public ingress listener before the replacement
// binds it; this is the regression test for the production "address already
// in use" restart loop.
func TestForwardTCPRecoversAfterOriginRestart(t *testing.T) {
	for _, transport := range []string{"tcp", "stealth"} {
		t.Run(transport, func(t *testing.T) {
			backend := startEchoBackend(t)
			tunnelPort, entryPort := freePort(t), freePort(t)
			token := "forward-restart-token-0123456789abcdef"
			originCfg := baseServerConfig(transport, tunnelPort, 1, backend.addr, token)
			originCfg.Ports = nil
			edgeCfg := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
			edgeCfg.Ports = []string{fmt.Sprintf("%d=%s", entryPort, backend.addr)}

			edgeCtx, stopEdge := context.WithCancel(context.Background())
			edge := client.NewForwardEdge(edgeCfg, edgeCtx)

			startOrigin := func() (context.CancelFunc, <-chan struct{}) {
				originCtx, stopOrigin := context.WithCancel(context.Background())
				done := make(chan struct{})
				origin := server.NewForwardOrigin(originCfg, originCtx)
				go func() {
					defer close(done)
					origin.Start()
				}()
				return stopOrigin, done
			}

			stopOrigin, originDone := startOrigin()
			time.Sleep(300 * time.Millisecond)
			edgeDone := make(chan struct{})
			go func() { defer close(edgeDone); edge.Start() }()
			tun := &tunnel{Entry: fmt.Sprintf("127.0.0.1:%d", entryPort)}
			t.Cleanup(func() {
				stopEdge()
				stopOrigin()
				for name, done := range map[string]<-chan struct{}{"origin": originDone, "edge": edgeDone} {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Errorf("%s did not stop during cleanup", name)
					}
				}
			})
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("initial forward generation never became ready: %v", err)
			}

			stopOrigin()
			select {
			case <-originDone:
			case <-time.After(5 * time.Second):
				t.Fatal("old origin did not stop")
			}
			stopOrigin, originDone = startOrigin()
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("forward tunnel did not recover after origin restart: %v", err)
			}
		})
	}
}

func runForwardPair(t *testing.T, originCfg *config.ServerConfig, edgeCfg *config.ClientConfig, entryPort, tunnelPort int) *tunnel {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	origin := server.NewForwardOrigin(originCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); origin.Start() }()
	time.Sleep(300 * time.Millisecond)
	edge := client.NewForwardEdge(edgeCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); edge.Start() }()
	tun := &tunnel{Entry: fmt.Sprintf("127.0.0.1:%d", entryPort), TunnelPort: tunnelPort, cancel: cancel, wg: &wg}
	t.Cleanup(tun.Stop)
	return tun
}

func testForwardStreamTransport(t *testing.T, transport string) {
	backend := startEchoBackend(t)
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "forward-e2e-token-0123456789abcdef"

	origin := baseServerConfig(transport, tunnelPort, 1, backend.addr, token)
	if transport == "wss" || transport == "wssmux" {
		origin.TLSCertFile, origin.TLSKeyFile = testCert(t)
	}
	origin.Ports = nil // Kharej must not expose the user's ingress port.
	edge := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
	edge.Ports = []string{fmt.Sprintf("%d=%s", entryPort, backend.addr)}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	srv := server.NewForwardOrigin(origin, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); srv.Start() }()
	time.Sleep(300 * time.Millisecond)
	cli := client.NewForwardEdge(edge, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); cli.Start() }()

	tun := &tunnel{Entry: fmt.Sprintf("127.0.0.1:%d", entryPort), TunnelPort: tunnelPort, cancel: cancel, wg: &wg}
	t.Cleanup(tun.Stop)
	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("forward %s tunnel never carried traffic: %v", transport, err)
	}
	if err := tun.roundTrip(randomPayload(t, 512*1024)); err != nil {
		t.Fatalf("forward %s payload failed: %v", transport, err)
	}
}

func TestForwardUDP(t *testing.T) {
	backendAddr := startUDPEchoBackend(t)
	tunnelPort, entryPort := freePort(t), freePort(t)
	token := "forward-udp-token-0123456789abcdef"
	origin := baseServerConfig("udp", tunnelPort, 1, backendAddr, token)
	origin.Ports = nil
	edge := baseClientConfig("udp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
	edge.Ports = []string{fmt.Sprintf("%d=%s", entryPort, backendAddr)}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	srv := server.NewForwardOrigin(origin, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); srv.Start() }()
	time.Sleep(300 * time.Millisecond)
	cli := client.NewForwardEdge(edge, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); cli.Start() }()

	entry := fmt.Sprintf("127.0.0.1:%d", entryPort)
	payload := []byte("forward-udp-datagram")
	deadline := time.Now().Add(tunnelReadyTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := udpRoundTrip(entry, payload); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("forward UDP tunnel never carried a datagram: %v", lastErr)
}
