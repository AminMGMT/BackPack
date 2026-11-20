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

	// The handshake from before v1.7.0 is refused, and told why.
	t.Run("a client from before v1.7.0 is told it is too old", func(t *testing.T) {
		msg, sig := answer(t, "not-the-real-token", utils.SG_Chan)
		if sig != utils.SG_Refused || msg != utils.RefusedTooOld {
			t.Errorf("a pre-1.7 handshake answered (%q, %d), want (%q, SG_Refused)",
				msg, sig, utils.RefusedTooOld)
		}
	})

	// And v2.0.0's handshake under the wrong token is refused as a failed
	// proof — the token itself never crossed the wire to be compared.
	t.Run("a wrong token on the v2.0.0 handshake is named", func(t *testing.T) {
		h, _ := network.PeerKeyFrom("not-the-real-token").NewPeerHello("tcp")
		msg, sig := answer(t, h.Encode(), utils.SG_ChanAuth)
		if sig != utils.SG_Refused || msg != utils.RefusedAuth {
			t.Errorf("a v2.0.0 claim under the wrong token answered (%q, %d), want (%q, SG_Refused)",
				msg, sig, utils.RefusedAuth)
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
