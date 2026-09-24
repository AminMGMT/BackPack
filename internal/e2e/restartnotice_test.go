package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/server"
)

// A server that stops cleanly tells its client, on every transport.
//
// TestServerRestartRecovery proves the client finds a restarted server again,
// but with an 8-second keepalive, which is also how long a datagram client
// waits for word from the server before giving up on it. At the production
// default of 75 that wait is 112 seconds, and it is exactly what a clean
// restart used to cost on quic and kcp: over TCP the closed socket is a FIN the
// client reads at once, while over UDP the goodbye was dropped with the socket
// and the client heard nothing. Measured before the fix: 113s on both.
//
// So this runs at the real keepalive and allows fifteen seconds — the client's
// own retry backoff, not its idle deadline.
var noticeWithin = 15 * time.Second

func TestAStoppedServerIsNoticedAtOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("restarts a server under each datagram transport")
	}
	for _, tc := range []struct {
		transport string
		// clientRestarted replaces the client first, which makes the server
		// restart its own run to adopt the new one. The goodbye has to survive
		// a run that began mid-life, not only the first: on quic the stop
		// after such a restart lost the race with the listener every time.
		clientRestarted bool
	}{
		{"kcp", false}, {"quic", false}, {"tcp", false},
		// quic only: that is where the bug was, and an in-process client
		// restart on kcp trips over the ghost-client artefact described in
		// TestEveryTransportComesBackAfterTheTunnelRestarts — it fails the
		// same way on code without any of this, so it would test the harness.
		{"quic", true},
	} {
		transport := tc.transport
		name := transport
		if tc.clientRestarted {
			name += "/after-a-client-restart"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			backend := startEchoBackend(t)
			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "restart-notice-token-0123456789"

			cliCfg := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
			cliCfg.Keepalive = 75 // the production default, see cmd/defaults.go

			clientCtx, stopClient := context.WithCancel(context.Background())
			defer stopClient()

			srvCtx, stopServer := context.WithCancel(context.Background())
			var srvWG sync.WaitGroup
			srv := server.NewServer(baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token), srvCtx)
			srvWG.Add(1)
			go func() { defer srvWG.Done(); srv.Start() }()
			time.Sleep(300 * time.Millisecond)

			tun := startClientAgainst(t, clientCtx, cliCfg, entryPort, tunnelPort)
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("%s never came up: %v", transport, err)
			}
			if tc.clientRestarted {
				stopClient()
				tun.wg.Wait()
				clientCtx, stopClient = context.WithCancel(context.Background())
				defer stopClient()
				tun = startClientAgainst(t, clientCtx, cliCfg, entryPort, tunnelPort)
				// Generous, and not what is under test: the old client's
				// goroutines outlive its Start by a moment and hold the same
				// token, so they can win the claim first (see the ghost note in
				// TestEveryTransportComesBackAfterTheTunnelRestarts). Slow under
				// the race detector, never wrong.
				if err := tun.waitReady(60 * time.Second); err != nil {
					t.Fatalf("%s never came back after the client restarted: %v", transport, err)
				}
			}

			stopServer()
			srv.Stop()
			srvWG.Wait()
			waitPortFree(t, tunnelPort, 15*time.Second)
			waitPortFree(t, entryPort, 15*time.Second)

			srv2Ctx, stopServer2 := context.WithCancel(context.Background())
			defer stopServer2()
			srv2 := server.NewServer(baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token), srv2Ctx)
			var srv2WG sync.WaitGroup
			srv2WG.Add(1)
			go func() { defer srv2WG.Done(); srv2.Start() }()
			defer func() { stopServer2(); srv2.Stop(); srv2WG.Wait() }()

			began := time.Now()
			if err := tun.waitReady(noticeWithin); err != nil {
				t.Fatalf("%s: the client did not notice the server had gone within 15s "+
					"(its keepalive deadline is 112s, which is what it would otherwise wait): %v",
					transport, err)
			}
			t.Logf("%s recovered %s after the new server started", transport, time.Since(began).Round(100*time.Millisecond))
		})
	}
}
