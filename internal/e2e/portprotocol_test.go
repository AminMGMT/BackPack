package e2e

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/client"
	"github.com/backpack/backpack/internal/server"
)

// port_protocol = "udp" forwards the ports over UDP alone: the datagrams get
// through, and nothing listens for TCP on those ports — on the plain engine's
// tcp and on the transports of Backpack's own engine alike.
func TestUDPOnlyPortsCarryUDPAndOpenNoTCP(t *testing.T) {
	for _, transport := range []string{"tcp", "kcp"} {
		t.Run(transport, func(t *testing.T) {
			backend := startUDPEcho(t)
			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "udp-only-token-0123456789abcdefg"

			srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend, token)
			srvCfg.PortProtocol = "udp"
			cliCfg := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

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
			conn, err := net.Dial("udp", entry)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			deadline := time.Now().Add(tunnelReadyTimeout)
			got := false
			for time.Now().Before(deadline) && !got {
				_, _ = conn.Write([]byte("ping"))
				_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				buf := make([]byte, 16)
				if n, err := conn.Read(buf); err == nil && string(buf[:n]) == "ping" {
					got = true
				}
			}
			if !got {
				t.Fatal("a UDP-only forwarded port never carried a datagram")
			}

			ln, err := net.Listen("tcp", entry)
			if err != nil {
				t.Fatalf("port_protocol = \"udp\" still opened a TCP listener on the forwarded port: %v", err)
			}
			ln.Close()
		})
	}
}
