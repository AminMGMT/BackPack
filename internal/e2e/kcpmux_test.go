package e2e

import (
	"fmt"
	"testing"
)

// A KCP tunnel must come up with no mux_version configured — which is what a
// menu-generated config actually has, because the config writer never emits
// mux_version for a KCP transport.
//
// This is the regression that a real deployment hit and the test suite did not:
// the harness pins mux_version to 2 everywhere, so it never exercised the auto
// value a real config carries. With auto (0) passed straight to smux — which
// rejects any version that is not 1 or 2 — every session failed to build with
// "unsupported protocol version", and the tunnel connected and then carried
// nothing. KCP cannot negotiate the version, so it has to resolve auto to a
// valid number itself; this proves it does.
//
// It runs on kcp rather than xdi because xdi needs a raw socket (root), but the
// two share the very code under test — the mux version is resolved in the same
// KCP transport both use.
func TestKCPComesUpWithNoMuxVersionConfigured(t *testing.T) {
	// Every mux transport that cannot negotiate — KCP over UDP, and the
	// websocket-mux transports — has to resolve the auto value itself. xdi is
	// the same code as kcp but needs a raw socket, so kcp stands in for it here.
	for _, transport := range []string{"kcp", "wsmux"} {
		t.Run(transport, func(t *testing.T) {
			backend := startEchoBackend(t)
			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "muxauto-token-0123456789abcdef"

			srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)
			cliCfg := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

			// The whole point: unset, as a real config without a written
			// mux_version is. Zero is "auto", and smux rejects it unless the
			// transport resolves it to 1 or 2 first.
			srvCfg.MuxVersion = 0
			cliCfg.MuxVersion = 0

			tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("a %s tunnel with no mux_version never carried traffic: %v", transport, err)
			}
		})
	}
}
