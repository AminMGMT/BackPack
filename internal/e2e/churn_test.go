package e2e

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/server"
)

// A server that stays up while its client comes and goes does not accumulate
// anything — on either side of the process.
//
// Every client that leaves ends one of the server's generations, and the next
// client starts another in the same process — the lifecycle ADR 0001 describes.
// A generation that leaves a goroutine or a descriptor behind is invisible for
// days and then is the reason a server that has been up for a month runs out of
// file descriptors. So the same server serves eight clients in turn, each one
// carrying traffic before it goes, and what the process holds afterwards is
// compared with what it held after the first.
//
// udp is not here: its forwarded ports are UDP, and this churns a TCP user
// through each client.
//
// Deliberately not parallel: it counts the whole process's goroutines, and Go
// runs every sequential test before any parallel one starts.
func TestAServerOutlivesItsClientsWithoutLeaking(t *testing.T) {
	if testing.Short() {
		t.Skip("churns eight clients per transport")
	}
	for _, transport := range []string{"tcp", "tcpmux", "ws", "wsmux", "kcp", "quic"} {
		t.Run(transport, func(t *testing.T) {
			backend := startEchoBackend(t)
			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "churn-token-0123456789abcdef"

			srvCtx, stopServer := context.WithCancel(context.Background())
			var srvWG sync.WaitGroup
			srv := server.NewServer(baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token), srvCtx)
			srvWG.Add(1)
			go func() { defer srvWG.Done(); srv.Start() }()
			defer func() { stopServer(); srv.Stop(); srvWG.Wait() }()
			time.Sleep(300 * time.Millisecond)

			cliCfg := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
			visit := func(i int) {
				ctx, cancel := context.WithCancel(context.Background())
				tun := startClientAgainst(t, ctx, cliCfg, entryPort, tunnelPort)
				tun.cancel = cancel
				if err := tun.waitReady(tunnelReadyTimeout); err != nil {
					t.Fatalf("client %d never came up: %v", i, err)
				}
				if err := tun.roundTrip(randomPayload(t, 64*1024)); err != nil {
					t.Fatalf("client %d could not carry traffic: %v", i, err)
				}
				tun.Stop()
			}

			visit(0)
			base := settle()
			for i := 1; i < 8; i++ {
				visit(i)
			}
			after := settle()

			// A small allowance for what is between generations when the count
			// is taken. A leak of two goroutines or two descriptors per client
			// is past it after seven clients; the leaks this was written for
			// were thirty and more per client.
			if after.goroutines > base.goroutines+12 {
				t.Errorf("goroutines grew from %d to %d over seven clients", base.goroutines, after.goroutines)
			}
			if after.openFiles > base.openFiles+8 {
				t.Errorf("open files grew from %d to %d over seven clients", base.openFiles, after.openFiles)
			}
		})
	}
}

// settle waits for the process to stop changing — a generation winds down
// behind the client that ended it — and samples it.
func settle() sample {
	prev := takeSample()
	for i := 0; i < 20; i++ {
		time.Sleep(250 * time.Millisecond)
		runtime.GC()
		cur := takeSample()
		if cur == prev {
			return cur
		}
		prev = cur
	}
	return prev
}
