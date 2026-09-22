package e2e

import (
	"fmt"
	"testing"
	"time"
)

// A first address that is not there must not hold the tunnel up.
//
// The endpoint list used to be walked one at a time: dial the current address,
// and on failure rotate. The race gives the preferred address a head start and
// starts the next one if that expires, so a dead first address costs a stagger
// rather than a dial timeout.
//
// What is asserted here is the end-to-end claim — the tunnel comes up promptly
// with a bad address in front of a good one — rather than a timing detail. The
// timing is held at the seam, in network.Race's own tests, where a dialler can
// be made to hang on command.
//
// A note on why this uses a refused address rather than a blackholed one: a
// sandbox that NATs everything answers on addresses that are not routed on a
// real network, so the blackhole case cannot be produced reliably here. It is
// the case network.Race's unit tests cover directly.
func TestABadFirstAddressDoesNotStopTheTunnel(t *testing.T) {
	backend := startEchoBackend(t)
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "race-token-0123456789abcdefghi"

	srvCfg := baseServerConfig("tcp", tunnelPort, entryPort, backend.addr, token)

	// A loopback port with nothing on it: refused immediately, every time.
	dead := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cliCfg := baseClientConfig("tcp",
		dead, token, []string{fmt.Sprintf("127.0.0.1:%d", tunnelPort)})

	start := time.Now()
	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("the tunnel never came up past a bad first address: %v", err)
	}
	t.Logf("came up in %v with a dead address first", time.Since(start).Round(time.Millisecond))

	if err := tun.roundTrip(randomPayload(t, 32*1024)); err != nil {
		t.Fatalf("the tunnel came up and carried nothing: %v", err)
	}
}

// And the race must not break the ordinary case: one address, nothing to race,
// no second connection opened.
func TestASingleAddressStillConnectsNormally(t *testing.T) {
	backend := startEchoBackend(t)
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "race-token-0123456789abcdefghi"

	srvCfg := baseServerConfig("tcp", tunnelPort, entryPort, backend.addr, token)
	cliCfg := baseClientConfig("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("a plain single-address tunnel regressed: %v", err)
	}
	if err := tun.roundTrip(randomPayload(t, 32*1024)); err != nil {
		t.Fatalf("a plain single-address tunnel carried nothing: %v", err)
	}
}
