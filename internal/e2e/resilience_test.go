package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/server"
)

// TestFailoverToBackupAddress covers the feature that keeps a tunnel alive when
// a server IP gets filtered: the client is given a primary address that answers
// nothing and a backup that works, and must find its own way to the backup.
func TestFailoverToBackupAddress(t *testing.T) {
	for _, transport := range []string{"tcp", "tcpmux", "kcp"} {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			backend := startEchoBackend(t)

			// A port with nothing behind it stands in for a filtered address.
			deadPort := freePort(t)

			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "failover-token-0123456789abcdef"

			srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)
			cliCfg := baseClientConfig(transport,
				fmt.Sprintf("127.0.0.1:%d", deadPort), // primary: dead
				token,
				[]string{fmt.Sprintf("127.0.0.1:%d", tunnelPort)}, // backup: alive
			)

			tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
			if err := tun.waitReady(45 * time.Second); err != nil {
				t.Fatalf("%s never failed over to the backup address: %v", transport, err)
			}

			if err := tun.roundTrip(randomPayload(t, 32*1024)); err != nil {
				t.Fatalf("traffic failed after failing over on %s: %v", transport, err)
			}
		})
	}
}

// TestServerRestartRecovery is the auto-recovery assertion: when the server
// disappears and comes back — a reboot, a crash, an update — the client must
// reconnect on its own, without anyone touching it.
func TestServerRestartRecovery(t *testing.T) {
	for _, transport := range []string{"tcp", "tcpmux", "kcp"} {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			backend := startEchoBackend(t)

			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "recovery-token-0123456789abcdef"

			srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)
			cliCfg := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

			// Client runs for the whole test; the server is replaced underneath it.
			clientCtx, stopClient := context.WithCancel(context.Background())
			defer stopClient()

			srvCtx, stopServer := context.WithCancel(context.Background())
			var srvWG sync.WaitGroup
			srv := server.NewServer(srvCfg, srvCtx)
			srvWG.Add(1)
			go func() { defer srvWG.Done(); srv.Start() }()

			time.Sleep(300 * time.Millisecond)
			tun := startClientAgainst(t, clientCtx, cliCfg, entryPort, tunnelPort)

			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("%s tunnel never came up: %v", transport, err)
			}

			// Take the server away.
			stopServer()
			srv.Stop()
			srvWG.Wait()
			// The replacement binds the same port, so wait for the old listener
			// to actually let go of it rather than guessing at a sleep.
			waitPortFree(t, tunnelPort, 15*time.Second)
			waitPortFree(t, entryPort, 15*time.Second)

			// Bring an identical server back on the same port.
			srv2Ctx, stopServer2 := context.WithCancel(context.Background())
			defer stopServer2()
			var srv2WG sync.WaitGroup
			srv2 := server.NewServer(
				baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token), srv2Ctx)
			srv2WG.Add(1)
			go func() { defer srv2WG.Done(); srv2.Start() }()

			// The client must find it again by itself.
			if err := tun.waitReady(60 * time.Second); err != nil {
				t.Fatalf("%s did not recover after the server came back: %v", transport, err)
			}
			if err := tun.roundTrip(randomPayload(t, 32*1024)); err != nil {
				t.Fatalf("traffic failed after recovery on %s: %v", transport, err)
			}

			stopServer2()
			srv2.Stop()
		})
	}
}

// TestNetworkPartitionRecovery simulates the link going away and coming back
// while both ends stay running — a route flap or a temporary block, which is
// far more common on this path than a server actually dying.
func TestNetworkPartitionRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("slow by design — skipped under -short")
	}
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	const token = "partition-token-0123456789abcdef"

	// 0% loss to begin with: the relay is here to be switched off, not to drop.
	relay := startLossyRelay(t, fmt.Sprintf("127.0.0.1:%d", tunnelPort), 0)
	tun := startTunnelVia(t, "kcp", backend, tunnelPort, entryPort, token, relay.Addr)

	if err := tun.roundTrip(randomPayload(t, 16*1024)); err != nil {
		t.Fatalf("tunnel did not work before the partition: %v", err)
	}

	// Cut the path: every datagram in both directions is now discarded.
	relay.SetLoss(100)
	time.Sleep(4 * time.Second)

	// Restore it.
	relay.SetLoss(0)

	if err := tun.waitReady(60 * time.Second); err != nil {
		t.Fatalf("the tunnel never recovered after the path was restored: %v", err)
	}
	if err := tun.roundTrip(randomPayload(t, 16*1024)); err != nil {
		t.Fatalf("traffic failed after the partition healed: %v", err)
	}
}
