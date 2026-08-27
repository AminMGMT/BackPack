package e2e

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/server"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"
)

// A refused handshake has to say why it was refused.
//
// This is the failure a report arrived with: tunnels that would not come up,
// and a log insisting the far server was out of date and should be upgraded.
// The server was fine — the two ends had different tokens.
//
// The server refused by closing the connection, which reaches the client as
// "failed to read message length from net.Conn: EOF". So does a server too old
// to understand the signal. So does a server that has already given its control
// channel to somebody else. Three faults, one symptom, and the client named the
// one that was least likely and most expensive to act on.
//
// The giveaway was in the report's own log: the client fell back to the older
// handshake and went on getting EOF, which an actually-old server would have
// answered. Nothing in the code was reading that.
//
// This drives the real server with a hand-written client, because what is on
// trial is what the server puts on the wire.
func TestTheServerSaysWhyItRefusedAHandshake(t *testing.T) {
	tunnelPort, entryPort := freePort(t), freePort(t)
	backend := startEchoBackend(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	srv := server.NewServer(
		baseServerConfig("tcp", tunnelPort, entryPort, backend.addr, "the-real-token"), ctx)
	wg.Add(1)
	go func() { defer wg.Done(); srv.Start() }()
	t.Cleanup(func() { cancel(); wg.Wait() })
	time.Sleep(500 * time.Millisecond)

	answer := func(t *testing.T, token string, signal byte) (string, byte) {
		t.Helper()
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), 5*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		if err := utils.SendBinaryTransportString(conn, token, signal); err != nil {
			t.Fatalf("sending the handshake: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		msg, sig, err := utils.ReceiveBinaryTransportString(conn)
		if err != nil {
			t.Fatalf("the server closed without answering (%v) — this is the silence "+
				"that made a wrong token indistinguishable from an old server", err)
		}
		return msg, sig
	}

	t.Run("a wrong token is named as a wrong token", func(t *testing.T) {
		msg, sig := answer(t, "not-the-real-token", utils.SG_ChanV2)
		if sig != utils.SG_Refused {
			t.Fatalf("the server answered signal %d, want SG_Refused", sig)
		}
		if msg != utils.RefusedBadToken {
			t.Errorf("the refusal reads %q, want %q", msg, utils.RefusedBadToken)
		}
	})

	// The legacy handshake must be refused just as clearly. A client that has
	// fallen back is exactly the one most in need of being told.
	t.Run("the older handshake is refused just as clearly", func(t *testing.T) {
		msg, sig := answer(t, "not-the-real-token", utils.SG_Chan)
		if sig != utils.SG_Refused || msg != utils.RefusedBadToken {
			t.Errorf("a legacy handshake with a wrong token answered (%q, %d), want (%q, SG_Refused)",
				msg, sig, utils.RefusedBadToken)
		}
	})

	// And the right token still gets a real answer, or the fix has broken the
	// thing it was protecting.
	t.Run("the right token is still answered normally", func(t *testing.T) {
		msg, sig := answer(t, "the-real-token", utils.SG_ChanV2)
		if sig == utils.SG_Refused {
			t.Fatalf("the correct token was refused: %q", msg)
		}
		if msg == "" {
			t.Error("the server answered an empty payload to a valid handshake")
		}
	})
}

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
	session, err := network.KCPDial(fmt.Sprintf("127.0.0.1:%d", tunnelPort),
		"the-real-token", settings)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer session.Close()

	if err := utils.SendBinaryTransportString(session, "not-the-real-token", utils.SG_ChanV2); err != nil {
		t.Fatalf("sending the handshake: %v", err)
	}
	_ = session.SetReadDeadline(time.Now().Add(8 * time.Second))
	msg, sig, err := utils.ReceiveBinaryTransportString(session)
	if err != nil {
		t.Fatalf("the kcp server closed without answering (%v) — the silence that "+
			"makes a wrong token look like an old server", err)
	}
	if sig != utils.SG_Refused || msg != utils.RefusedBadToken {
		t.Errorf("the kcp server answered (%q, %d), want (%q, SG_Refused)",
			msg, sig, utils.RefusedBadToken)
	}
}
