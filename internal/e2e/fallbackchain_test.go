package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
)

// The transport fallback chain, end to end.
//
// This is the product's headline claim — a tunnel that keeps working when the
// path changes — and until now the engine would not do it: a tunnel pinned to a
// filtered transport retried that transport for ever and the operator was the
// failover mechanism.
//
// The tests below use the strongest available form of "this carrier is
// blocked": the other end is not speaking it at all. A client dialling quic at
// a server that only listens for tcp cannot complete a handshake no matter how
// long it waits, which is exactly the shape of a filtered carrier and needs no
// packet filter to arrange.

// The client is configured for a transport the server does not speak, with a
// fallback to one it does. The tunnel must come up on the fallback and carry
// data — without anyone touching it.
func TestClientFallsBackToATransportTheServerSpeaks(t *testing.T) {
	backend := startEchoBackend(t)
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "fallback-token-0123456789abcdefg"

	// The server speaks tcp and only tcp.
	srvCfg := baseServerConfig("tcp", tunnelPort, entryPort, backend.addr, token)

	// The client's first choice is quic, which will never be answered.
	cliCfg := baseClientConfig("quic", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
	cliCfg.FallbackTransports = []config.TransportType{config.TCP}
	// Two candidates, so each gets half the dwell — floored at five seconds, so
	// a slow dial is never mistaken for a blocked one. Keep the dwell small
	// enough that the test is not dominated by waiting.
	cliCfg.FallbackDwell = 10

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)

	// The first candidate has to time out and the second has to come up, so
	// this legitimately takes longer than an ordinary tunnel start.
	if err := tun.waitReady(60 * time.Second); err != nil {
		t.Fatalf("the chain never reached a working transport: %v", err)
	}
	if err := tun.roundTrip(randomPayload(t, 64*1024)); err != nil {
		t.Fatalf("data did not survive the fallback transport: %v", err)
	}
}

// The other direction: the server's first choice is a transport the client will
// not dial, so the server has to rotate to the one the client is waiting on.
//
// This is the half that is easy to leave out. A chain on the client alone does
// nothing, because the server is only listening for one carrier — and the
// reason the two ends meet without negotiating is that the server dwells while
// the client sweeps, which is only exercised when the server is the one moving.
func TestServerRotatesToTheTransportTheClientIsUsing(t *testing.T) {
	backend := startEchoBackend(t)
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "fallback-token-0123456789abcdefg"

	// The server starts on quic and falls back to tcp.
	srvCfg := baseServerConfig("quic", tunnelPort, entryPort, backend.addr, token)
	srvCfg.FallbackTransports = []config.TransportType{config.TCP}
	srvCfg.FallbackDwell = 8

	// The client only ever speaks tcp.
	cliCfg := baseClientConfig("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)

	if err := tun.waitReady(60 * time.Second); err != nil {
		t.Fatalf("the server never rotated to the client's transport: %v", err)
	}
	if err := tun.roundTrip(randomPayload(t, 64*1024)); err != nil {
		t.Fatalf("data did not survive after the server rotated: %v", err)
	}
}

// Both ends rotating at once, started out of step: the server's first choice
// is the client's second and the client's first is the server's second. Neither
// end is told where the other is, so the only thing that brings them together
// is the cadence — the server dwells on a candidate while the client sweeps the
// whole list — and this is the test that actually exercises it.
//
// It also proves the thing that makes the design safe on the server: a
// candidate has to let go of the forwarded ports before the next one binds
// them, or the second candidate fails to start and the tunnel never recovers.
//
// (Giving both ends the same list in the same order would prove nothing: they
// would agree on the first candidate and never rotate. The first version of
// this test did that and passed in half a second.)
func TestTwoRotatingEndsStartedOutOfStepConverge(t *testing.T) {
	backend := startEchoBackend(t)
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "fallback-token-0123456789abcdefg"

	srvCfg := baseServerConfig("quic", tunnelPort, entryPort, backend.addr, token)
	srvCfg.FallbackTransports = []config.TransportType{config.TCP}
	srvCfg.FallbackDwell = 8

	cliCfg := baseClientConfig("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
	cliCfg.FallbackTransports = []config.TransportType{config.QUIC}
	cliCfg.FallbackDwell = 8

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)

	if err := tun.waitReady(90 * time.Second); err != nil {
		t.Fatalf("two rotating ends never converged: %v", err)
	}
	if err := tun.roundTrip(randomPayload(t, 64*1024)); err != nil {
		t.Fatalf("data did not survive after both ends rotated: %v", err)
	}
}

// The default configuration must be untouched by any of this. A tunnel with no
// fallback list is the overwhelming majority of deployments, and the chain has
// to be exactly the code it replaced for them.
func TestNoFallbackListBehavesExactlyAsBefore(t *testing.T) {
	backend := startEchoBackend(t)
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "fallback-token-0123456789abcdefg"

	srvCfg := baseServerConfig("tcp", tunnelPort, entryPort, backend.addr, token)
	cliCfg := baseClientConfig("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("a plain tunnel regressed: %v", err)
	}
	if err := tun.roundTrip(randomPayload(t, 64*1024)); err != nil {
		t.Fatalf("a plain tunnel regressed: %v", err)
	}
}
