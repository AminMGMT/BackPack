package e2e

import (
	"fmt"
	"net"
	"testing"
)

// secondLoopbackAvailable reports whether this machine can bind a loopback
// address other than 127.0.0.1.
//
// On Linux the whole 127.0.0.0/8 is local, so 127.0.0.2 and 127.0.0.3 are
// bindable without configuring anything — which is what makes the two-address
// case reproducible on an ordinary runner rather than only on a server that
// genuinely has two public addresses. Elsewhere it is not, and a skip is honest
// where a failure would be misleading.
func secondLoopbackAvailable(t *testing.T) bool {
	t.Helper()
	for _, host := range []string{"127.0.0.2", "127.0.0.3"} {
		l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			return false
		}
		l.Close()
	}
	return true
}

// The scenario the feature was asked for, end to end.
//
// A two-address Iran server wants the control channel and the user-facing port
// to be the same number — 443 on both, so the control channel blends in as
// HTTPS like everything else. The forwarded ports have taken an address for a
// while ("IP_B:443=127.0.0.1:2053"); the control port could not, because every
// path that built a BindAddr wrote 0.0.0.0 and joined the port to it. Both ends
// therefore asked for 0.0.0.0:443 and the second one got
// `bind: address already in use`.
//
// This runs both halves on one port number at once and then moves real traffic
// through the forwarded one, which is the only way to show that the control
// channel is genuinely up rather than merely bound.
func TestTheControlPortAndAnExposedPortShareOnePortNumber(t *testing.T) {
	if !secondLoopbackAvailable(t) {
		t.Skip("this machine has only one usable loopback address")
	}

	const (
		controlHost = "127.0.0.2" // stands in for IP_A
		exposedHost = "127.0.0.3" // stands in for IP_B
	)

	for _, transport := range []string{"tcp", "tcpmux", "ws"} {
		t.Run(transport, func(t *testing.T) {
			backend := startEchoBackend(t)

			// One number for both. Any free port will do; what matters is that
			// the control channel and the forwarded port are given the same
			// one on different addresses.
			shared := freePort(t)
			token := "dual-address-token-0123456789abc"

			srvCfg := baseServerConfig(transport, shared, shared, backend.addr, token)
			srvCfg.BindAddr = net.JoinHostPort(controlHost, fmt.Sprint(shared))
			srvCfg.Ports = []string{
				fmt.Sprintf("%s=%s", net.JoinHostPort(exposedHost, fmt.Sprint(shared)), backend.addr),
			}

			cliCfg := baseClientConfig(transport,
				net.JoinHostPort(controlHost, fmt.Sprint(shared)), token, nil)

			tun := runPair(t, srvCfg, cliCfg, shared, shared)
			// The harness assumes the entry is on 127.0.0.1; here it is
			// deliberately not, which is the whole point.
			tun.Entry = net.JoinHostPort(exposedHost, fmt.Sprint(shared))

			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("%s: the control channel on %s:%d and the exposed port on %s:%d "+
					"did not both come up: %v",
					transport, controlHost, shared, exposedHost, shared, err)
			}
			if err := tun.roundTrip(randomPayload(t, 64*1024)); err != nil {
				t.Fatalf("%s: both ends bound but nothing crossed: %v", transport, err)
			}
		})
	}
}

// A control port pinned to one address must not answer on another.
//
// Binding one address and still being reachable everywhere would mean the pin
// did nothing — and on the server this is for, "everywhere" includes the
// address a different service is meant to own.
func TestAPinnedControlPortIsNotReachableOnAnotherAddress(t *testing.T) {
	if !secondLoopbackAvailable(t) {
		t.Skip("this machine has only one usable loopback address")
	}

	backend := startEchoBackend(t)
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	const token = "pinned-control-token-0123456789a"

	srvCfg := baseServerConfig("tcp", tunnelPort, entryPort, backend.addr, token)
	srvCfg.BindAddr = net.JoinHostPort("127.0.0.2", fmt.Sprint(tunnelPort))

	cliCfg := baseClientConfig("tcp",
		net.JoinHostPort("127.0.0.2", fmt.Sprint(tunnelPort)), token, nil)

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("the pinned tunnel never came up: %v", err)
	}

	// The address it was told to bind, and only that one. 127.0.0.3 is this
	// machine's too, so a wildcard bind would answer here.
	elsewhere := net.JoinHostPort("127.0.0.3", fmt.Sprint(tunnelPort))
	ln, err := net.Listen("tcp", elsewhere)
	if err != nil {
		t.Fatalf("%s should be free — the tunnel was pinned to 127.0.0.2, "+
			"so binding one address has not actually pinned anything: %v", elsewhere, err)
	}
	ln.Close()
}
