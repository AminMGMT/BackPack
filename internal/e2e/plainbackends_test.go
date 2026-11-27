package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/client"
	"github.com/backpack/backpack/internal/server"
)

// On the plain engine a backend list ("a|b") reached the kharej's dialer
// whole and failed on every connection, and an IPv6 target was refused as a
// bad port. Both now work on tcp, tcpmux, ws and wsmux: connections take the
// backends in turn, a backend that is down is passed over, and a bracketed
// IPv6 address is dialled as written.
func TestThePlainTransportsTakeBackendListsAndIPv6Targets(t *testing.T) {
	for _, tr := range []string{"tcp", "tcpmux", "ws", "wsmux"} {
		t.Run(tr, func(t *testing.T) {
			t.Parallel()
			t.Run("both backends serve", func(t *testing.T) {
				b1, b2 := countingEcho(t, "127.0.0.1:0"), countingEcho(t, "127.0.0.1:0")
				entry := plainTunnelTo(t, tr, b1.addr+"|"+b2.addr)
				for i := range 6 {
					if err := poolRoundTrip(entry, fmt.Appendf(nil, "list-%d", i)); err != nil {
						t.Fatalf("connection %d through a two-backend list: %v", i, err)
					}
				}
				if b1.n.Load() == 0 || b2.n.Load() == 0 {
					t.Fatalf("one backend served everything: %d and %d connections", b1.n.Load(), b2.n.Load())
				}
			})
			t.Run("a backend that is down is passed over", func(t *testing.T) {
				alive := countingEcho(t, "127.0.0.1:0")
				dead := fmt.Sprintf("127.0.0.1:%d", freePort(t))
				entry := plainTunnelTo(t, tr, dead+"|"+alive.addr)
				for i := range 6 {
					if err := poolRoundTrip(entry, fmt.Appendf(nil, "dead-%d", i)); err != nil {
						t.Fatalf("connection %d failed although one backend is up: %v", i, err)
					}
				}
			})
			t.Run("an IPv6 target", func(t *testing.T) {
				l, err := net.Listen("tcp", "[::1]:0")
				if err != nil {
					t.Skip("no IPv6 loopback here")
				}
				l.Close()
				b := countingEcho(t, "[::1]:0")
				entry := plainTunnelTo(t, tr, b.addr)
				if err := poolRoundTrip(entry, []byte("over ipv6")); err != nil {
					t.Fatalf("an IPv6 target did not carry: %v", err)
				}
			})
		})
	}
}

type countedEcho struct {
	addr string
	n    atomic.Int32
}

// countingEcho is an echo backend that counts its connections.
func countingEcho(t *testing.T, at string) *countedEcho {
	t.Helper()
	l, err := net.Listen("tcp", at)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	e := &countedEcho{addr: l.Addr().String()}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			e.n.Add(1)
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return e
}

// plainTunnelTo runs a tunnel whose one forwarded port goes to target, and
// returns that port once it carries traffic.
func plainTunnelTo(t *testing.T, tr, target string) string {
	t.Helper()
	tunnelPort, entryPort := freePort(t), freePort(t)
	token := "plain-backends-token-0123456789"
	srvCfg := baseServerConfig(tr, tunnelPort, entryPort, target, token)
	srvCfg.Ports = []string{fmt.Sprintf("%d=%s", entryPort, target)}
	cliCfg := baseClientConfig(tr, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

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
	deadline := time.Now().Add(tunnelReadyTimeout)
	var err error
	for time.Now().Before(deadline) {
		if err = poolRoundTrip(entry, []byte("ready?")); err == nil {
			return entry
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("the tunnel to %s never carried traffic: %v", target, err)
	return ""
}
