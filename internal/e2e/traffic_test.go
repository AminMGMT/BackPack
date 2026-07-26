package e2e

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

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

			// An echo sends the payload and gets it back, so at least the payload
			// itself crosses the tunnel in each direction.
			//
			// This used to assert only "not zero", which is too weak to be worth
			// much: a relay that counted nothing but its own handshake reported
			// twelve bytes for a quarter-megabyte transfer and passed. The
			// figure the panel shows is the point, so check the figure.
			if gotIn < payload {
				t.Errorf("%s recorded %d bytes in after %d bytes were echoed — the traffic is not being counted",
					transport, gotIn, payload)
			}
			if gotOut < payload {
				t.Errorf("%s recorded %d bytes out after %d bytes were echoed — the traffic is not being counted",
					transport, gotOut, payload)
			}
			t.Logf("%s: in %d, out %d for a %d byte round trip", transport, gotIn, gotOut, payload)
		})
	}
}

// Traffic has to be counted while it is flowing, not when the connection ends.
//
// The relay hands its two sockets to the kernel to splice between, and the
// kernel reports one total when the copy finishes. Counting only that total is
// exact and useless: a tunnel carrying a long download or a video call would
// report nothing for as long as it lasted and then jump by the whole figure at
// the end. The panel would show an idle tunnel, and so would the throughput
// signal the connection pool scales on — which exists to notice exactly those
// long-lived streams.
//
// So this holds one connection open, and checks the counters have moved before
// it closes.
func TestTrafficIsCountedWhileTheConnectionIsStillOpen(t *testing.T) {
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	const token = "inflight-token-0123456789abcdef"

	srvCfg := baseServerConfig("tcp", tunnelPort, entryPort, backend.addr, token)
	cliCfg := baseClientConfig("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("tunnel never came up: %v", err)
	}

	conn, err := net.DialTimeout("tcp", tun.Entry, 5*time.Second)
	if err != nil {
		t.Fatalf("dial entry port: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}

	beforeIn, beforeOut := metrics.Traffic()

	// Enough to cross the reporting threshold several times over, echoed in
	// pieces so the connection stays open throughout.
	const piece = 256 * 1024
	const pieces = 4
	payload := randomPayload(t, piece)
	for i := 0; i < pieces; i++ {
		errCh := make(chan error, 1)
		go func() { _, err := conn.Write(payload); errCh <- err }()

		got := make([]byte, piece)
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatalf("piece %d never came back: %v", i, err)
		}
		if err := <-errCh; err != nil {
			t.Fatalf("piece %d write: %v", i, err)
		}
	}

	// Still open, on purpose — that is the whole point.
	afterIn, afterOut := metrics.Traffic()
	gotIn, gotOut := afterIn-beforeIn, afterOut-beforeOut

	const sent = piece * pieces
	if gotIn < sent {
		t.Errorf("counted %d bytes in while %d were carried on a connection that is still open", gotIn, sent)
	}
	if gotOut < sent {
		t.Errorf("counted %d bytes out while %d were carried on a connection that is still open", gotOut, sent)
	}
	t.Logf("in %d, out %d counted mid-connection for %d bytes echoed", gotIn, gotOut, sent)
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
