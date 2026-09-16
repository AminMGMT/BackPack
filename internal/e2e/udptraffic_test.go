package e2e

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/client"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/server"
)

// The udp transport carried traffic and reported none of it.
//
// TestTrafficIsCountedOnEveryTransport says "every transport" and lists six,
// and udp is not among them — it cannot be, because that test forwards a TCP
// echo backend through the shared harness and udp needs a datagram path on both
// ends. So the one transport with no coverage was the one transport with no
// counters, and it stayed that way through every release: neither CountedConn
// nor AddBytes appeared in either of its files, because it never hands out a
// net.Conn for the wrapper to go around.
//
// What that looked like from outside was not a missing feature. It was a tunnel
// carrying gigabytes and reading 0 B in, 0 B out in the panel, in the CLI, in
// the Telegram report and on the traffic chart — an idle tunnel, as far as
// anything that displays it could tell.
func TestTrafficIsCountedOnTheUDPTransport(t *testing.T) {
	backendAddr := startUDPEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	const token = "udptraffic-token-0123456789abcd"

	srvCfg := baseServerConfig("udp", tunnelPort, entryPort, backendAddr, token)
	cliCfg := baseClientConfig("udp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

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

	// Datagrams are unreliable and the tunnel takes a moment, so the link is
	// proved with retries first and only then measured.
	small := []byte("udp-traffic-readiness-probe")
	deadline := time.Now().Add(tunnelReadyTimeout)
	ready := false
	for time.Now().Before(deadline) {
		if err := udpRoundTrip(entry, small); err == nil {
			ready = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !ready {
		t.Fatal("udp tunnel never carried a datagram")
	}

	beforeIn, beforeOut := metrics.Traffic()

	// Datagram-sized rather than the 256 KB the TCP test uses: one UDP payload
	// has to fit in a datagram, so the figure is made up of many of them.
	const packet = 1200
	const packets = 40
	carried := 0
	for i := 0; i < packets; i++ {
		if err := udpRoundTrip(entry, randomPayload(t, packet)); err == nil {
			carried++
		}
	}
	if carried == 0 {
		t.Fatal("no datagram round-tripped during the measurement")
	}

	afterIn, afterOut := metrics.Traffic()
	gotIn, gotOut := afterIn-beforeIn, afterOut-beforeOut

	// Loss is allowed for — this is UDP, and a datagram the kernel drops was
	// never carried. What is not allowed for is zero, or a figure that could
	// only be a handshake: each round trip that succeeded crossed the tunnel
	// once in each direction with a full payload.
	want := uint64(carried) * packet
	if gotIn < want {
		t.Errorf("udp recorded %d bytes in after %d datagrams of %d bytes round-tripped — "+
			"the traffic is not being counted", gotIn, carried, packet)
	}
	if gotOut < want {
		t.Errorf("udp recorded %d bytes out after %d datagrams of %d bytes round-tripped — "+
			"the traffic is not being counted", gotOut, carried, packet)
	}
	t.Logf("udp: in %d, out %d for %d/%d datagrams of %d bytes", gotIn, gotOut, carried, packets, packet)
}

// max_connections and bandwidth_mbps were accepted by the menu, saved into the
// TOML, rendered, and shown in the panel for a udp tunnel — and the struct the
// transport was built from had nowhere to put them, so nothing was ever
// enforced. An operator capping a shared udp tunnel got a cap that read back
// correctly everywhere and did nothing.
//
// A "connection" here is a source address rather than a socket, because that is
// the only thing a connectionless protocol has that means the same thing: one
// peer's flow through the tunnel.
func TestTheUDPConnectionLimitIsEnforced(t *testing.T) {
	backendAddr := startUDPEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	const token = "udplimit-token-0123456789abcdef"
	const cap = 2

	srvCfg := baseServerConfig("udp", tunnelPort, entryPort, backendAddr, token)
	srvCfg.MaxConnections = cap
	cliCfg := baseClientConfig("udp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

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

	// The first flow also proves the tunnel is up. It holds its socket open, so
	// its slot stays taken for the rest of the test.
	held, err := holdUDPFlow(t, entry, tunnelReadyTimeout)
	if err != nil {
		t.Fatalf("udp tunnel never came up: %v", err)
	}
	defer held.Close()

	// Each of these is a distinct source address, so each is a distinct flow.
	carried := 1
	for i := 0; i < cap*3; i++ {
		c, err := holdUDPFlow(t, entry, 800*time.Millisecond)
		if err != nil {
			continue
		}
		defer c.Close()
		carried++
	}

	if carried > cap {
		t.Errorf("%d flows were forwarded at once with max_connections = %d — the cap "+
			"is accepted, saved and displayed, and not applied", carried, cap)
	}
	if carried < cap {
		t.Errorf("only %d flows were forwarded with max_connections = %d — a cap that "+
			"refuses below its own limit is worse than none", carried, cap)
	}
}

// holdUDPFlow opens one source address, proves a datagram round-trips through
// it, and returns the socket still open so the flow keeps its slot.
func holdUDPFlow(t *testing.T, entry string, within time.Duration) (net.Conn, error) {
	t.Helper()
	deadline := time.Now().Add(within)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.Dial("udp", entry)
		if err != nil {
			lastErr = err
			continue
		}
		conn.SetDeadline(time.Now().Add(1500 * time.Millisecond))
		payload := []byte("hold-this-flow-open")
		if _, err := conn.Write(payload); err != nil {
			conn.Close()
			lastErr = err
			continue
		}
		got := make([]byte, len(payload))
		if _, err := conn.Read(got); err != nil {
			conn.Close()
			lastErr = err
			time.Sleep(150 * time.Millisecond)
			continue
		}
		// Cleared, or every later use of this socket inherits the probe's.
		conn.SetDeadline(time.Time{})
		return conn, nil
	}
	return nil, lastErr
}
