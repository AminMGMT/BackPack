package e2e

import (
	"fmt"
	"testing"

	"github.com/backpack/backpack/internal/metrics"
)

// Only KCP ever reported traffic, because kcp-go happens to keep its own byte
// counters. Every other transport showed "0 B in, 0 B out" while carrying
// gigabytes — the numbers were not wrong, they were absent, which is worse
// because it looks like an idle tunnel rather than a missing feature.
//
// These run real tunnels and check that bytes actually land in the counters.
func TestTrafficIsCountedOnEveryTransport(t *testing.T) {
	for _, transport := range []string{"tcp", "tcpmux", "kcp", "ws", "wsmux", "stealth"} {
		t.Run(transport, func(t *testing.T) {
			backend := startEchoBackend(t)

			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "traffic-token-0123456789abcdefg"

			srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)
			cliCfg := baseClientConfig(transport,
				fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

			tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("tunnel never came up: %v", err)
			}

			beforeIn, beforeOut := metrics.Traffic()

			const payload = 256 * 1024
			if err := tun.roundTrip(randomPayload(t, payload)); err != nil {
				t.Fatalf("round trip failed: %v", err)
			}

			afterIn, afterOut := metrics.Traffic()
			gotIn, gotOut := afterIn-beforeIn, afterOut-beforeOut

			if gotIn == 0 {
				t.Errorf("%s recorded no inbound traffic after %d bytes were echoed", transport, payload)
			}
			if gotOut == 0 {
				t.Errorf("%s recorded no outbound traffic after %d bytes were echoed", transport, payload)
			}
			t.Logf("%s: in %d, out %d for a %d byte round trip", transport, gotIn, gotOut, payload)
		})
	}
}

// The counters must not double-count. Both ends run in this process, so each
// byte crosses the tunnel once but touches two local sockets; wrapping the
// local side as well would roughly double every figure.
func TestTrafficIsNotDoubleCounted(t *testing.T) {
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	const token = "nodouble-token-0123456789abcdef"

	srvCfg := baseServerConfig("tcp", tunnelPort, entryPort, backend.addr, token)
	cliCfg := baseClientConfig("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("tunnel never came up: %v", err)
	}

	const payload = 512 * 1024
	beforeIn, beforeOut := metrics.Traffic()
	if err := tun.roundTrip(randomPayload(t, payload)); err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
	afterIn, afterOut := metrics.Traffic()

	total := (afterIn - beforeIn) + (afterOut - beforeOut)

	// An echo sends the payload and gets it back, so ~2x crosses the tunnel,
	// and both ends of the tunnel live in this process, so ~4x is counted.
	// Anything beyond that means a connection is wrapped twice.
	const ceiling = payload * 6
	if total > ceiling {
		t.Errorf("counted %d bytes for a %d byte round trip — connections are being wrapped more than once",
			total, payload)
	}
	t.Logf("%d bytes counted for a %d byte round trip", total, payload)
}
