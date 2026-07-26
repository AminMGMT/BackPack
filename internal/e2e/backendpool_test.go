package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/client"
	"github.com/backpack/backpack/internal/server"
)

// A forwarded port with two backends must carry traffic end to end: the
// pipe-separated list has to survive the port mapping, the control protocol,
// the client's address resolution and the backend pool, and connections must
// echo correctly whichever backend serves them.
//
// The health-check timing (dropping a dead backend) is covered by unit tests in
// the transport package; this proves the whole wiring across a real tunnel.
func TestBackendPoolCarriesTrafficAcrossBackends(t *testing.T) {
	b1 := startEchoBackend(t)
	b2 := startEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "pool-token-0123456789abcdefghi"

	srvCfg := baseServerConfig("tcp", tunnelPort, entryPort, b1.addr, token)
	// Two backends behind the one forwarded port.
	srvCfg.Ports = []string{fmt.Sprintf("%d=%s|%s", entryPort, b1.addr, b2.addr)}
	cliCfg := baseClientConfig("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

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

	// Several sequential connections; round-robin means both backends serve,
	// and each must echo intact.
	deadline := time.Now().Add(tunnelReadyTimeout)
	for time.Now().Before(deadline) {
		if err := poolRoundTrip(entry, []byte("ready?")); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	for i := 0; i < 6; i++ {
		msg := []byte(fmt.Sprintf("backend-pool-%d", i))
		if err := poolRoundTrip(entry, msg); err != nil {
			t.Fatalf("connection %d did not echo through the pool: %v", i, err)
		}
	}
}

func poolRoundTrip(entry string, payload []byte) error {
	c, err := net.DialTimeout("tcp", entry, 3*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write(payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil {
		return err
	}
	if string(got) != string(payload) {
		return fmt.Errorf("echo mismatch: %q != %q", got, payload)
	}
	return nil
}
