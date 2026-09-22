package e2e

import (
	"fmt"
	"net"
	"testing"
	"time"
)

// ipv6Available reports whether this machine can actually use IPv6 loopback.
// CI runners sometimes cannot, and a skipped test is honest where a failing one
// would be misleading.
func ipv6Available(t *testing.T) bool {
	t.Helper()
	l, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// TestIPv6Tunnel runs a whole tunnel over IPv6 — the control channel, the pool
// and the forwarded port. Address handling is where IPv6 usually breaks: a
// literal address glued to a port without brackets simply will not resolve.
func TestIPv6Tunnel(t *testing.T) {
	if !ipv6Available(t) {
		t.Skip("no IPv6 loopback on this machine")
	}

	// Every transport, not three of them.
	//
	// It used to be tcp, tcpmux and kcp — the three somebody happened to try.
	// Address handling is per transport here: each builds its own dialler and
	// its own listener, and the bug this catches is a literal glued to a port
	// without brackets, which is a line of code that exists separately in each
	// of them. Three of ten covered is a guard that names the transports
	// already known to work.
	//
	// udp routes through carriesDatagrams for the same reason it does in the
	// main matrix: its forwarded port is a datagram port, so dialling TCP at it
	// asks the wrong question.
	for _, transport := range allReverseTransports {
		t.Run(transport, func(t *testing.T) {
			if transport == "udp" {
				carriesDatagramsOver(t, transport, "::1", "::")
				return
			}
			backend := startEchoBackend(t) // backend stays on IPv4; the tunnel is what is under test

			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "ipv6-token-0123456789abcdefghi"

			srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)
			// Bind the IPv6 wildcard, exactly as the setup menu does when the
			// user answers yes to "listen on IPv6 as well".
			srvCfg.BindAddr = net.JoinHostPort("::", fmt.Sprint(tunnelPort))

			// The client dials the IPv6 literal, which is the case that needs
			// brackets throughout the address handling.
			cliCfg := baseClientConfig(transport,
				net.JoinHostPort("::1", fmt.Sprint(tunnelPort)), token, nil)

			tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("%s tunnel over IPv6 never carried traffic: %v", transport, err)
			}
			if err := tun.roundTrip(randomPayload(t, 64*1024)); err != nil {
				t.Fatalf("%s over IPv6 failed to move data: %v", transport, err)
			}
		})
	}
}

// TestIPv6FailoverAddress checks that a fallback list holding IPv6 literals is
// parsed and used correctly — bracket handling is easy to get wrong in a list.
func TestIPv6FailoverAddress(t *testing.T) {
	if !ipv6Available(t) {
		t.Skip("no IPv6 loopback on this machine")
	}
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	deadPort := freePort(t)
	const token = "ipv6-failover-token-0123456789ab"

	srvCfg := baseServerConfig("tcp", tunnelPort, entryPort, backend.addr, token)
	srvCfg.BindAddr = net.JoinHostPort("::", fmt.Sprint(tunnelPort))

	cliCfg := baseClientConfig("tcp",
		net.JoinHostPort("::1", fmt.Sprint(deadPort)), // primary: nothing there
		token,
		[]string{net.JoinHostPort("::1", fmt.Sprint(tunnelPort))}, // backup: alive
	)

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	if err := tun.waitReady(45 * time.Second); err != nil {
		t.Fatalf("failover to an IPv6 backup address never completed: %v", err)
	}
	if err := tun.roundTrip(randomPayload(t, 16*1024)); err != nil {
		t.Fatalf("traffic failed after IPv6 failover: %v", err)
	}
}
