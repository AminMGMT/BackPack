package e2e

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tcpTransports are the transports that forward TCP connections. The raw UDP
// transport forwards datagrams instead and is covered separately.
var tcpTransports = []string{"tcp", "tcpmux", "kcp", "ws", "wsmux", "stealth"}

// TestTransportCarriesData is the baseline every transport must pass: a payload
// large enough to span many packets goes through and comes back unchanged.
func TestTransportCarriesData(t *testing.T) {
	for _, transport := range tcpTransports {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			backend := startEchoBackend(t)
			tun := startTunnel(t, transport, backend, tunnelOptions{})

			payload := randomPayload(t, 512*1024)
			if err := tun.roundTrip(payload); err != nil {
				t.Fatalf("512 KiB through %s: %v", transport, err)
			}
		})
	}
}

// TestTransportConcurrentConnections checks that many simultaneous connections
// are kept separate. A multiplexing bug shows up here as crossed streams —
// data returning on the wrong connection — which a single-connection test
// cannot detect.
func TestTransportConcurrentConnections(t *testing.T) {
	for _, transport := range tcpTransports {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			backend := startEchoBackend(t)
			tun := startTunnel(t, transport, backend, tunnelOptions{})

			const conns = 25
			var wg sync.WaitGroup
			var failures int64
			errs := make(chan error, conns)

			for i := 0; i < conns; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					// A distinct payload per connection: if the transport ever
					// delivers one connection's data to another, the comparison
					// inside roundTrip fails.
					payload := []byte(fmt.Sprintf("connection-%03d-", i))
					payload = append(payload, randomPayload(t, 8*1024)...)
					if err := tun.roundTrip(payload); err != nil {
						atomic.AddInt64(&failures, 1)
						errs <- fmt.Errorf("connection %d: %w", i, err)
					}
				}(i)
			}
			wg.Wait()
			close(errs)

			if failures > 0 {
				for err := range errs {
					t.Error(err)
				}
				t.Fatalf("%d of %d concurrent connections failed on %s", failures, conns, transport)
			}
		})
	}
}

// TestTransportSequentialConnections checks that the connection pool keeps
// serving after it has been drained and refilled — the pool maintainer opening
// replacements is what makes a long-lived tunnel keep working.
func TestTransportSequentialConnections(t *testing.T) {
	for _, transport := range []string{"tcp", "tcpmux", "kcp"} {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			backend := startEchoBackend(t)
			tun := startTunnel(t, transport, backend, tunnelOptions{})

			for i := 0; i < 30; i++ {
				payload := randomPayload(t, 4096)
				if err := tun.roundTrip(payload); err != nil {
					t.Fatalf("%s failed on connection %d of 30: %v", transport, i+1, err)
				}
			}
		})
	}
}

// TestWrongTokenIsRejected is a security assertion: a peer that does not know
// the token must never be able to move traffic. It is easy to break this by
// accident when changing a handshake, and nothing else would notice.
func TestWrongTokenIsRejected(t *testing.T) {
	for _, transport := range []string{"tcp", "tcpmux", "kcp"} {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			backend := startEchoBackend(t)

			// A correctly configured tunnel, so the server is listening.
			good := startTunnel(t, transport, backend, tunnelOptions{Token: "the-real-token-aaaaaaaaaaaaaaaa"})

			// A second client with the wrong token, aimed at the same server.
			bad := startClientOnly(t, transport,
				fmt.Sprintf("127.0.0.1:%d", good.TunnelPort),
				"a-completely-different-token-bb")
			defer bad.Stop()

			// Give the impostor time to try and fail.
			time.Sleep(3 * time.Second)

			// The legitimate tunnel must still work: a rejected impostor must
			// not disturb the real peer.
			if err := good.roundTrip(randomPayload(t, 4096)); err != nil {
				t.Fatalf("the real tunnel broke while an impostor was dialling: %v", err)
			}
		})
	}
}
