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

// The kcp re-adopt path had the fix but no coverage, while udp and quic both
// had a test for theirs. These close that gap. They run on kcp rather than xdi
// because xdi needs a raw ICMP socket (root) — it is the same transport code
// with a different packet layer, so kcp stands in for it here.

// kcpTestSettings are the session knobs a stand-in client must dial with. Both
// ends have to agree: the FEC shards are not negotiated, and the token doubles
// as the AES key, so a mismatch means the server cannot decode the packets at
// all and the test would pass for the wrong reason.
func kcpTestSettings() network.KCPSettings {
	k := turboKCP()
	return network.KCPSettings{
		MTU:          k.MTU,
		Interval:     k.Interval,
		Resend:       k.Resend,
		NoDelay:      k.NoDelay,
		NoCongestion: k.NoCongestion,
		SndWnd:       k.SndWnd,
		RcvWnd:       k.RcvWnd,
		AckNoDelay:   k.AckNoDelay,
		DataShards:   k.DataShards,
		ParityShards: k.ParityShards,
	}
}

// TestKCPServerReadoptsRestartedClient is the re-adopt assertion: a client that
// restarts on its own — a crash, a service restart, an edit — must get its
// tunnel back without anyone touching the server.
//
// The point is that the server cannot notice the old client left. KCP carries
// no RST, the control-channel read blocks with no deadline, and a heartbeat
// write to a silent peer is buffered rather than refused — so the dead session
// stays "established" for this run and the only thing that ever arrives is the
// new client's claim. Before the fix that claim was discarded as a duplicate
// and the tunnel stayed dead until the server was restarted by hand.
//
// Two things this test does deliberately, because without either of them it
// passes against the broken server as well:
//
//   - The old client is cut off through a relay before it stops, not simply
//     stopped. A clean shutdown sends SG_Closed on the control channel, which
//     the server acts on by restarting — recovery that owes nothing to re-adopt.
//   - The restart is a real client, not a synthetic claim. A stand-in claim that
//     gets discarded leaves the live control channel untouched, so the tunnel
//     keeps working either way.
func TestKCPServerReadoptsRestartedClient(t *testing.T) {
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	const token = "kcp-readopt-token-0123456789abc"

	srvCfg := baseServerConfig("kcp", tunnelPort, entryPort, backend.addr, token)

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
	cli1Cfg := baseClientConfig("kcp", relay.Addr, token, nil)

	cli1Ctx, stopClient1 := context.WithCancel(context.Background())
	defer stopClient1()
	tun := startClientAgainst(t, cli1Ctx, cli1Cfg, entryPort, tunnelPort)
	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("kcp tunnel never came up: %v", err)
	}

	// Cut the path, then stop the client behind it. Nothing it says on the way
	// out — SG_Closed included — can reach the server, so the server is left
	// holding a control channel it still believes in.
	relay.SetLoss(100)
	time.Sleep(500 * time.Millisecond)
	stopClient1()
	time.Sleep(500 * time.Millisecond)

	// The restarted client, reaching the server directly.
	cli2Cfg := baseClientConfig("kcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
	cli2Ctx, stopClient2 := context.WithCancel(context.Background())
	defer stopClient2()
	startClientAgainst(t, cli2Ctx, cli2Cfg, entryPort, tunnelPort)

	// The entry port is the server's, so the original handle still measures it.
	if err := tun.waitReady(45 * time.Second); err != nil {
		t.Fatalf("server never adopted the restarted client: %v", err)
	}
	if err := tun.roundTrip(randomPayload(t, 4096)); err != nil {
		t.Fatalf("traffic failed after the re-adopt: %v", err)
	}
}

// TestKCPRejectsWrongTokenWithoutRestarting is the other half of the re-adopt
// design: a peer that reaches the handshake but does not know the token must
// not be able to disturb the running tunnel. If the re-adopt decision were made
// on the bare session instead of the token, this impostor would force an
// endless restart loop — a trivial denial of service.
//
// The session is dialled with the real token so its packets decrypt and the
// announcement actually arrives; it is the announced token that is wrong. A
// session dialled with the wrong token would be dropped by the cipher long
// before the token check, and would prove nothing about it.
func TestKCPRejectsWrongTokenWithoutRestarting(t *testing.T) {
	backend := startEchoBackend(t)
	const token = "the-real-kcp-token-aaaaaaaaaaaaa"
	tun := startTunnel(t, "kcp", backend, tunnelOptions{Token: token})

	session, err := network.KCPDial(fmt.Sprintf("127.0.0.1:%d", tun.TunnelPort), token, kcpTestSettings())
	if err != nil {
		t.Fatalf("cannot dial the kcp control port: %v", err)
	}
	defer session.Close()
	network.ApplyKCPSettings(session, kcpTestSettings())
	if err := utils.SendBinaryTransportString(session, "a-completely-different-token-bb", utils.SG_Chan); err != nil {
		t.Fatalf("cannot send the control claim: %v", err)
	}

	// Give the server a moment to (wrongly) act on it.
	time.Sleep(3 * time.Second)

	// The real tunnel must be undisturbed.
	if err := tun.roundTrip(randomPayload(t, 4096)); err != nil {
		t.Fatalf("the real kcp tunnel broke while an impostor was dialling: %v", err)
	}
}
