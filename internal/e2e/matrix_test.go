package e2e

import (
	"fmt"
	"testing"
	"time"
)

// One place that answers "does every transport still carry traffic".
//
// The suite already exercises most of them, spread across a dozen files that
// each ask a narrower question — loss recovery, mux versions, TLS, the pool.
// What it did not have is the flat answer: for every transport this build
// ships, does a tunnel come up and do bytes survive the round trip. That is the
// question asked after any change to the data path, and answering it meant
// reading the transport list out of six files and hoping none had been missed.
//
// tcpTransports in transport_test.go lists seven. The full set is ten, and the
// three it leaves out are not exotic: wss and wssmux terminate TLS, and udp
// carries datagrams. Each has its own file for its own reason; none of them
// appeared in a list of "every transport".
//
// The raw-socket carriers — xdi, pck, spoof, sni — are deliberately absent.
// They need CAP_NET_RAW and a packet socket, which a test process does not
// have, and pretending otherwise with a skip that never runs would be worse
// than saying so here. They are exercised in a network namespace instead; see
// docs and the spoof tester.

// allReverseTransports is every transport the reverse tunnel can be configured
// with that can run without elevated privileges.
var allReverseTransports = []string{
	"tcp",     // plain
	"tcpmux",  // smux over tcp
	"stealth", // tcp with a Noise record layer
	"ws",      // an HTTP upgrade
	"wss",     // the same, with TLS
	"wsmux",   // smux over ws
	"wssmux",  // smux over wss
	"kcp",     // reliable, over udp
	"quic",    // quic streams
	"udp",     // datagrams
}

// Every transport: the tunnel comes up and a payload survives it byte for byte.
func TestEveryTransportEstablishesAndCarriesData(t *testing.T) {
	for _, transport := range allReverseTransports {
		t.Run(transport, func(t *testing.T) {
			// udp is the one whose forwarded port is a datagram port rather
			// than a stream, because the flow it carries is a flow of
			// datagrams. Dialling TCP at it is asking the wrong question — and
			// is what this test did on its first run.
			if transport == "udp" {
				carriesDatagrams(t, transport)
				return
			}

			backend := startEchoBackend(t)
			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "matrix-token-0123456789abcdefgh"

			srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)
			cliCfg := baseClientConfig(transport,
				fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

			tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("%s: the tunnel never carried traffic: %v", transport, err)
			}
			// Big enough to span many packets, so a transport that works for a
			// single small write and falls over on a stream is caught.
			if err := tun.roundTrip(randomPayload(t, 256*1024)); err != nil {
				t.Fatalf("%s: data did not survive the round trip: %v", transport, err)
			}
		})
	}
}

// The same matrix, restarted. A transport that comes up once and cannot come up
// again is a transport that works until the first reload — and reload is not
// rare here: the engine watches the config file and restarts itself in place
// whenever it changes.
func TestEveryTransportComesBackAfterTheTunnelRestarts(t *testing.T) {
	if testing.Short() {
		t.Skip("restarts every transport twice")
	}
	for _, transport := range allReverseTransports {
		t.Run(transport, func(t *testing.T) {
			backend := startEchoBackend(t)
			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "matrix-restart-token-0123456789a"

			for attempt := 1; attempt <= 2; attempt++ {
				if transport == "udp" {
					// Same reason as above: its forwarded port is a datagram
					// port. carriesDatagrams brings its own ports up and down.
					carriesDatagrams(t, transport)
					continue
				}
				func() {
					srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)
					cliCfg := baseClientConfig(transport,
						fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

					tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
					if err := tun.waitReady(tunnelReadyTimeout); err != nil {
						t.Fatalf("%s: run %d never came up: %v", transport, attempt, err)
					}
					if err := tun.roundTrip(randomPayload(t, 32*1024)); err != nil {
						t.Fatalf("%s: run %d carried nothing: %v", transport, attempt, err)
					}
					tun.Stop() // release the ports for the next run
				}()
			}
		})
	}
}

// carriesDatagrams is the same claim for a transport whose forwarded port is
// UDP: a datagram sent to the entry comes back from the backend unchanged.
//
// Retried rather than waited on, because datagrams are unreliable by
// definition and the first one may legitimately be dropped while the tunnel is
// still pairing. That is the same readiness pattern the stream harness uses,
// over a protocol that cannot promise delivery.
func carriesDatagrams(t *testing.T, transport string) {
	t.Helper()
	backendAddr := startUDPEchoBackend(t)
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "matrix-udp-token-0123456789abcd"

	srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backendAddr, token)
	cliCfg := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	_ = tun // stopped by the harness

	entry := fmt.Sprintf("127.0.0.1:%d", entryPort)
	payload := []byte("matrix-datagram-roundtrip-check")

	deadline := time.Now().Add(tunnelReadyTimeout)
	var last error
	for time.Now().Before(deadline) {
		if err := udpRoundTrip(entry, payload); err == nil {
			return
		} else {
			last = err
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s: never carried a datagram: %v", transport, last)
}
