package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/server"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"
)

// TestQUICServerReadoptsRestartedClient proves the re-adopt path: a client that
// restarts on its own re-dials with a fresh control claim, and the server —
// which still thinks its old control channel is live — adopts the new client
// and the tunnel comes back, with no manual restart. The same guarantee the udp
// and kcp transports carry, and the reason the re-adopt decision sits on the
// token check rather than the bare connection.
//
// Built the same way as the kcp one, and for the same reasons: the old client
// is cut off through a relay before it stops, so its SG_Closed never reaches the
// server, and the newcomer is a real client rather than a synthetic claim.
//
// The window is deliberately tight. QUIC, unlike KCP, does eventually notice a
// silent peer by itself — the negotiated idle timeout is three times the
// client's keepalive, so around 24s with this harness — and that recovery has
// nothing to do with re-adopt. Recovery driven by the claim lands in a few
// seconds; anything slower than the window below is the idle timeout doing the
// work instead, which is exactly what this test must not accept.
func TestQUICServerReadoptsRestartedClient(t *testing.T) {
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	const token = "quic-readopt-token-0123456789ab"

	srvCfg := baseServerConfig("quic", tunnelPort, entryPort, backend.addr, token)

	// The server runs untouched for the whole test; only the client is replaced.
	srvCtx, stopServer := context.WithCancel(context.Background())
	var srvWG sync.WaitGroup
	srv := server.NewServer(srvCfg, srvCtx)
	srvWG.Add(1)
	go func() { defer srvWG.Done(); srv.Start() }()
	t.Cleanup(func() { stopServer(); srv.Stop(); srvWG.Wait() })

	time.Sleep(300 * time.Millisecond)

	// The first client reaches the server through a relay that can be switched
	// off, which is how its disappearance is made silent.
	relay := startLossyRelay(t, fmt.Sprintf("127.0.0.1:%d", tunnelPort), 0)
	cli1Ctx, stopClient1 := context.WithCancel(context.Background())
	defer stopClient1()
	tun := startClientAgainst(t, cli1Ctx, baseClientConfig("quic", relay.Addr, token, nil), entryPort, tunnelPort)
	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("quic tunnel never came up: %v", err)
	}

	// Cut the path, then stop the client behind it, so the server is left
	// holding a control channel it still believes in.
	relay.SetLoss(100)
	time.Sleep(500 * time.Millisecond)
	stopClient1()
	time.Sleep(500 * time.Millisecond)

	// The restarted client, reaching the server directly.
	cli2Ctx, stopClient2 := context.WithCancel(context.Background())
	defer stopClient2()
	startClientAgainst(t, cli2Ctx,
		baseClientConfig("quic", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil), entryPort, tunnelPort)

	// The entry port is the server's, so the original handle still measures it.
	if err := tun.waitReady(15 * time.Second); err != nil {
		t.Fatalf("server never adopted the restarted client: %v", err)
	}
	if err := tun.roundTrip(randomPayload(t, 4096)); err != nil {
		t.Fatalf("traffic failed after the re-adopt: %v", err)
	}
}

// TestQUICRejectsWrongTokenWithoutRestarting is the other half of the re-adopt
// design: a peer that opens a QUIC connection but does not know the token must
// not be able to disturb the running tunnel. If the re-adopt decision were made
// on the bare connection instead of the token, this impostor would force an
// endless restart loop — a trivial denial of service.
func TestQUICRejectsWrongTokenWithoutRestarting(t *testing.T) {
	backend := startEchoBackend(t)
	tun := startTunnel(t, "quic", backend, tunnelOptions{Token: "the-real-quic-token-aaaaaaaaaaaa"})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// An impostor that completes the QUIC/TLS handshake but presents the wrong
	// token on the control stream.
	conn, err := network.QUICDial(ctx, fmt.Sprintf("127.0.0.1:%d", tun.TunnelPort), network.QUICSettings{})
	if err != nil {
		t.Fatalf("cannot dial the quic control port: %v", err)
	}
	defer conn.CloseWithError(0, "test done")

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("cannot open control stream: %v", err)
	}
	claim := network.NewQUICStreamConn(stream, conn)
	_ = utils.SendBinaryTransportString(claim, "a-completely-different-token-bb", utils.SG_Chan)

	// Give the server a moment to (wrongly) act on it.
	time.Sleep(3 * time.Second)

	// The real tunnel must be undisturbed.
	if err := tun.roundTrip(randomPayload(t, 4096)); err != nil {
		t.Fatalf("the real quic tunnel broke while an impostor was dialling: %v", err)
	}
}
