package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/metrics"
)

// The pool grows past the size that was configured for it, and from outside a
// grown pool and a leaking one look identical: a count of open sockets, larger
// than the number in the config. The panel can only tell them apart if the
// client says what it was aiming for and what it was carrying.
//
// Everything up to here is unit-tested in pieces. This is the join: a real
// tunnel, reported by the transport that maintains the pool, arriving in the
// snapshot the panel reads. It is the part that silently does nothing if the
// report is never called — which is exactly what a unit test cannot catch.
func TestPoolStateReachesTheSnapshotOnARealTunnel(t *testing.T) {
	metrics.ClearPool()
	t.Cleanup(metrics.ClearPool)

	backend := startEchoBackend(t)
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	const token = "poolreport-token-0123456789abc"

	srvCfg := baseServerConfig("tcp", tunnelPort, entryPort, backend.addr, token)
	cliCfg := baseClientConfig("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("tunnel never came up: %v", err)
	}

	// The pool reports on its own timer rather than per connection, so this
	// waits for a tick instead of assuming one has happened.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, configured, _ := metrics.PoolState(); configured > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	live, target, configured, _ := metrics.PoolState()
	if configured == 0 {
		t.Fatal("the pool never reported itself; the panel would show nothing for a tunnel that has one")
	}
	if configured != cliCfg.ConnectionPool {
		t.Errorf("reported a configured size of %d, but the tunnel was built with %d",
			configured, cliCfg.ConnectionPool)
	}
	// An idle tunnel has not grown, so the target is still what was asked for.
	// Reporting a target below the configured size would make the panel claim
	// the pool shrank past its floor.
	if target < configured {
		t.Errorf("target %d is below the configured %d", target, configured)
	}
	if live < 0 {
		t.Errorf("live connections = %d", live)
	}
	t.Logf("pool: %d live, target %d, configured %d", live, target, configured)
}
