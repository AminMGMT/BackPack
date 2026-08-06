package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

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
