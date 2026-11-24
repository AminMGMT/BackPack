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

// The same, on KCP.
//
// Adding a refusal to one transport and leaving the others silent is the exact
// pattern that put four separate faults in this codebase — something solved for
// one engine and missed for the rest. A datagram tunnel's operator is no better
// placed to guess at an unexplained failure than a TCP one's.
func TestTheKCPServerAlsoSaysWhyItRefused(t *testing.T) {
	tunnelPort, entryPort := freePort(t), freePort(t)
	backend := startEchoBackend(t)

	// baseServerConfig already carries turboKCP; the KCP settings are not
	// negotiated, so both ends have to agree on them by hand.
	srvCfg := baseServerConfig("kcp", tunnelPort, entryPort, backend.addr, "the-real-token")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	srv := server.NewServer(srvCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); srv.Start() }()
	t.Cleanup(func() { cancel(); wg.Wait() })
	time.Sleep(700 * time.Millisecond)

	// The crypto is keyed on the token, so a client with the wrong one cannot
	// even decrypt its way in. It is the token the server reads that is wrong
	// here, which is the case an operator actually hits: the same shared secret
	// typed differently at the two ends means neither of these matches.
	settings := network.KCPSettings{
		MTU: 1350, Interval: 20, Resend: 2, NoDelay: 1, NoCongestion: 1,
		SndWnd: 1024, RcvWnd: 1024, AckNoDelay: true,
		DataShards: 10, ParityShards: 3,
	}
	// The server is given 700 ms to bind, which a loaded CI runner did not
	// always manage: the first datagrams met a closed port, the ICMP answer
	// killed the connected KCP socket, and the read waited out its deadline.
	// So a session that hears nothing is dialled again, a few times, before
	// silence counts as the server's.
	var (
		msg string
		sig byte
		err error
	)
	for attempt := 1; ; attempt++ {
		session, derr := network.KCPDial(fmt.Sprintf("127.0.0.1:%d", tunnelPort),
			"the-real-token", settings)
		if derr != nil {
			t.Fatalf("dial: %v", derr)
		}
		if err := utils.SendBinaryTransportString(session, "not-the-real-token", utils.SG_ChanV2); err != nil {
			session.Close()
			t.Fatalf("sending the handshake: %v", err)
		}
		_ = session.SetReadDeadline(time.Now().Add(4 * time.Second))
		msg, sig, err = utils.ReceiveBinaryTransportString(session)
		session.Close()
		if err == nil || attempt == 4 {
			break
		}
	}
	if err != nil {
		t.Fatalf("the kcp server closed without answering (%v) — the silence that "+
			"makes a wrong token look like an old server", err)
	}
	if sig != utils.SG_Refused || msg != utils.RefusedBadToken {
		t.Errorf("the kcp server answered (%q, %d), want (%q, SG_Refused)",
			msg, sig, utils.RefusedBadToken)
	}
}
