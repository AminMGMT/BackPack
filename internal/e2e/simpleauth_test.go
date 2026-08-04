package e2e

import (
	"fmt"
	"testing"
	"time"
)

// With simple auth on both ends, a wss tunnel carries traffic over a real TLS
// session — proving the raw-token path works end to end, which is what a
// deployment behind a TLS-terminating proxy (NGINX) needs. The proxy itself is
// not simulated here; what is proven is that the credential the client sends
// and the credential the server accepts are the raw token, not a session-bound
// proof, so an intermediary that holds a different session no longer breaks it.
func TestWSSSimpleAuthCarriesTraffic(t *testing.T) {
	certPath, keyPath := testCert(t)

	for _, transport := range []string{"wss", "wssmux"} {
		t.Run(transport, func(t *testing.T) {
			backend := startEchoBackend(t)
			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "simpleauth-token-0123456789abcd"

			srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)
			srvCfg.TLSCertFile = certPath
			srvCfg.TLSKeyFile = keyPath
			srvCfg.SimpleAuth = true

			cliCfg := baseClientConfig(transport,
				fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
			cliCfg.SimpleAuth = true

			tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("%s with simple auth never carried traffic: %v", transport, err)
			}
			if err := tun.roundTrip(randomPayload(t, 64*1024)); err != nil {
				t.Fatalf("%s with simple auth failed to move data: %v", transport, err)
			}
		})
	}
}

// The two ends must agree. This is the security boundary that keeps simple auth
// opt-in: a bound server (simple auth off) must reject a client that offers the
// raw token, or the binding would protect nothing. Because both ends run over a
// real TLS session, this exercises the actual proof path a unit test cannot.
func TestWSSSimpleAuthMustAgree(t *testing.T) {
	certPath, keyPath := testCert(t)
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "simpleauth-token-0123456789abcd"

	// Server keeps the bound proof (default); client tries the raw token.
	srvCfg := baseServerConfig("wss", tunnelPort, entryPort, backend.addr, token)
	srvCfg.TLSCertFile = certPath
	srvCfg.TLSKeyFile = keyPath
	srvCfg.SimpleAuth = false

	cliCfg := baseClientConfig("wss",
		fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
	cliCfg.SimpleAuth = true

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	if err := tun.waitReady(12 * time.Second); err == nil {
		t.Fatal("a bound server accepted a client sending the raw token — the binding is not enforced")
	}
}
