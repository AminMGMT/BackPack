package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/server"
)

// A client that stops closes every connection it holds to the server at once,
// the idle pool included, while the server stays up.
//
// The pool waits on the server: a tcp or ws pool connection blocks reading for
// the next user, a mux one in AcceptStream. Neither looked at the client's
// context, so a stopped client in a process that keeps running (the manager,
// the tests) left its pool open until the server dropped it — and a pool
// connection that finished its handshake just as the client stopped could be
// held by the server for the next client. CI saw it as "client 5: 1
// connections to the tunnel port still open 10s after the client stopped".
func TestAStoppedClientClosesItsPoolAtOnce(t *testing.T) {
	for _, transport := range []string{"tcp", "tcpmux", "ws", "wsmux"} {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			backend := startEchoBackend(t)
			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "poolclose-token-0123456789abcdef"

			srvCtx, stopServer := context.WithCancel(context.Background())
			var srvWG sync.WaitGroup
			srv := server.NewServer(baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token), srvCtx)
			srvWG.Add(1)
			go func() { defer srvWG.Done(); srv.Start() }()
			defer func() { stopServer(); srv.Stop(); srvWG.Wait() }()
			time.Sleep(300 * time.Millisecond)

			cliCfg := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
			ctx, cancel := context.WithCancel(context.Background())
			tun := startClientAgainst(t, ctx, cliCfg, entryPort, tunnelPort)
			tun.cancel = cancel
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("the client never came up: %v", err)
			}
			if err := tun.roundTrip(randomPayload(t, 16*1024)); err != nil {
				t.Fatalf("the tunnel could not carry traffic: %v", err)
			}
			// Let the pool refill behind the connection just used.
			time.Sleep(time.Second)
			if n, err := establishedTo(tunnelPort); err == nil && n < 2 {
				t.Fatalf("only %d connections to the server before stopping; the pool never filled", n)
			}
			tun.Stop()
			if err := tun.waitGone(2 * time.Second); err != nil {
				t.Fatal(err)
			}
		})
	}
}
