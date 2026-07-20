package e2e

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConnectionLimitIsEnforced checks that a connection cap actually refuses
// connections past the limit, and — just as important — that it does not refuse
// connections below it.
func TestConnectionLimitIsEnforced(t *testing.T) {
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	const token = "limits-token-0123456789abcdefgh"
	const cap = 3

	srvCfg := baseServerConfig("tcpmux", tunnelPort, entryPort, backend.addr, token)
	srvCfg.MaxConnections = cap
	cliCfg := baseClientConfig("tcpmux", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("tunnel never came up: %v", err)
	}

	// Hold connections open so they occupy slots, and see how many can carry
	// data at once.
	var wg sync.WaitGroup
	var succeeded atomic.Int32
	release := make(chan struct{})

	for i := 0; i < cap*3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", tun.Entry, 3*time.Second)
			if err != nil {
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(6 * time.Second))

			// A round trip proves the connection was really admitted, not just
			// accepted by the kernel and then dropped.
			if _, err := conn.Write([]byte("ping")); err != nil {
				return
			}
			buf := make([]byte, 4)
			if _, err := conn.Read(buf); err != nil {
				return
			}
			succeeded.Add(1)
			<-release // hold the slot
		}()
	}

	// Give every attempt time to resolve one way or the other.
	time.Sleep(4 * time.Second)
	admitted := succeeded.Load()
	close(release)
	wg.Wait()

	if admitted == 0 {
		t.Fatal("the connection cap refused everything — a limit must still allow traffic up to it")
	}
	if int(admitted) > cap {
		t.Fatalf("%d connections carried data at once with the cap set to %d", admitted, cap)
	}
	t.Logf("cap %d: %d connections admitted simultaneously", cap, admitted)
}

// TestNoLimitsByDefault guards the more dangerous failure: a limit appearing
// where the user never asked for one.
func TestNoLimitsByDefault(t *testing.T) {
	backend := startEchoBackend(t)
	tun := startTunnel(t, "tcpmux", backend, tunnelOptions{})

	const conns = 20
	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < conns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := tun.roundTrip(randomPayload(t, 2048)); err != nil {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := failures.Load(); n > 0 {
		t.Fatalf("%d of %d connections failed with no limits configured", n, conns)
	}
}

// TestBandwidthLimitSlowsTransfer checks that a bandwidth cap has a real,
// measurable effect. It asserts only that the transfer cannot beat the cap by
// a wide margin — timing on a shared machine is too noisy for anything tighter,
// and a flaky test is worse than a loose one.
func TestBandwidthLimitSlowsTransfer(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-based — skipped under -short")
	}
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	const token = "bwlimit-token-0123456789abcdefg"
	const mbps = 8 // 1 MB/s

	srvCfg := baseServerConfig("tcpmux", tunnelPort, entryPort, backend.addr, token)
	srvCfg.BandwidthMbps = mbps
	cliCfg := baseClientConfig("tcpmux", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("tunnel never came up: %v", err)
	}

	// 2 MB echoed is 4 MB through the cap, so it cannot finish in under ~2s.
	payload := randomPayload(t, 2*1024*1024)
	start := time.Now()
	if err := tun.roundTrip(payload); err != nil {
		t.Fatalf("limited transfer failed: %v", err)
	}
	elapsed := time.Since(start)

	// Allow generous slack for the initial burst and scheduling noise.
	floor := 1200 * time.Millisecond
	if elapsed < floor {
		t.Fatalf("2 MB round trip finished in %v with an %d Mbit/s cap — the limit is not being applied",
			elapsed, mbps)
	}
	t.Logf("2 MB round trip took %v under an %d Mbit/s cap", elapsed, mbps)
}
