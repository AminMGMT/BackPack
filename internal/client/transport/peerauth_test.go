package transport

import (
	"net"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"
)

// fakeServer answers one claim on the far end of a pipe the way script says.
func fakeServer(t *testing.T, script func(conn net.Conn)) net.Conn {
	t.Helper()
	cli, srv := net.Pipe()
	go func() { defer srv.Close(); script(srv) }()
	t.Cleanup(func() { cli.Close() })
	return cli
}

// v2Server is the server half of the handshake, done by hand.
func v2Server(token, label string, last func(net.Conn, *network.PeerSession)) func(net.Conn) {
	return func(conn net.Conn) {
		k := network.PeerKeyFrom(token)
		msg, sig, err := utils.ReceiveBinaryTransportString(conn)
		if err != nil || sig != utils.SG_ChanAuth {
			return
		}
		h, ok := network.DecodePeerHello(msg)
		if !ok || !k.VerifyHello(h, label) {
			utils.SendBinaryTransportString(conn, utils.RefusedAuth, utils.SG_Refused)
			return
		}
		r, sess, _ := k.Reply(h, label)
		utils.SendBinaryTransportString(conn, r.Encode(), utils.SG_ChanAuth)
		msg, _, err = utils.ReceiveBinaryTransportString(conn)
		if err != nil || !sess.CheckConfirm(msg) {
			utils.SendBinaryTransportString(conn, utils.RefusedAuth, utils.SG_Refused)
			return
		}
		last(conn, sess)
	}
}

func TestAClaimProvesBothEndsAndLeavesASession(t *testing.T) {
	g := network.NewPeerGuard("tok-en-12345678", "", "")
	var server *network.PeerSession
	conn := fakeServer(t, v2Server("tok-en-12345678", "tcp", func(c net.Conn, s *network.PeerSession) {
		server = s
		utils.SendBinaryTransportString(c, "0", utils.SG_ChanAuth)
	}))
	reply, err := claimControl(conn, g, "tok-en-12345678", "tcp", utils.SG_ChanV2, silentLogger())
	if err != nil || reply.session == nil || reply.signal != utils.SG_ChanAuth {
		t.Fatalf("claim: %+v, %v", reply, err)
	}
	if reply.session.PoolTag() != server.PoolTag() {
		t.Fatal("the two ends hold different sessions")
	}
	if !g.Strict() {
		t.Fatal("a server that proved itself did not end the old handshake for this tunnel")
	}
}

// Something on the path that answers without the token is not the server,
// and is not a reason to fall back either.
func TestAnImpostorIsRefusedAndDoesNotCauseAFallback(t *testing.T) {
	g := network.NewPeerGuard("tok-en-12345678", "", "")
	for i := 0; i < 3; i++ {
		// It cannot check the hello, so it answers it blind.
		conn := fakeServer(t, func(c net.Conn) {
			msg, _, _ := utils.ReceiveBinaryTransportString(c)
			h, _ := network.DecodePeerHello(msg)
			r, _, _ := network.PeerKeyFrom("a-different-token").Reply(h, "tcp")
			utils.SendBinaryTransportString(c, r.Encode(), utils.SG_ChanAuth)
			time.Sleep(50 * time.Millisecond)
		})
		if _, err := claimControl(conn, g, "tok-en-12345678", "tcp", utils.SG_ChanV2, silentLogger()); err != errServerUnproven {
			t.Fatalf("an impostor's answer: %v", err)
		}
	}
	if g.UseLegacy() {
		t.Fatal("an impostor pushed the client back to the handshake that sends the token")
	}
}

// An older server closes on SG_ChanAuth. Twice in a row, and the client
// offers the previous handshake — unless the tunnel has proved v2 before.
func TestAnOlderServerGetsThePreviousHandshake(t *testing.T) {
	g := network.NewPeerGuard("tok-en-12345678", "", "")
	old := func(c net.Conn) { utils.ReceiveBinaryTransportString(c) } // reads, then closes
	for i := 0; i < network.PeerAuthMissThreshold; i++ {
		if _, err := claimControl(fakeServer(t, old), g, "tok-en-12345678", "tcp", utils.SG_ChanV2, silentLogger()); err == nil {
			t.Fatal("a closed claim succeeded")
		}
	}
	if !g.UseLegacy() {
		t.Fatal("two closed claims did not fall back")
	}
	conn := fakeServer(t, func(c net.Conn) {
		msg, sig, _ := utils.ReceiveBinaryTransportString(c)
		if sig != utils.SG_ChanV2 || msg != "tok-en-12345678" {
			return
		}
		utils.SendBinaryTransportString(c, "tok-en-12345678", utils.SG_ChanV2)
	})
	reply, err := claimControl(conn, g, "tok-en-12345678", "tcp", utils.SG_ChanV2, silentLogger())
	if err != nil || reply.session != nil || reply.ack != "tok-en-12345678" {
		t.Fatalf("the previous handshake: %+v, %v", reply, err)
	}

	// Required (or proved before): never.
	strict := network.NewPeerGuard("tok-en-12345678", network.PeerAuthRequired, "")
	for i := 0; i < 5; i++ {
		claimControl(fakeServer(t, old), strict, "tok-en-12345678", "tcp", utils.SG_ChanV2, silentLogger())
	}
	if strict.UseLegacy() {
		t.Fatal("peer_auth = required fell back")
	}
}

// A refusal is an answer: it says the server understood, so it is neither a
// miss nor proof.
func TestARefusalIsNeitherAMissNorProof(t *testing.T) {
	g := network.NewPeerGuard("tok-en-12345678", "", "")
	for i := 0; i < 3; i++ {
		conn := fakeServer(t, func(c net.Conn) {
			utils.ReceiveBinaryTransportString(c)
			utils.SendBinaryTransportString(c, utils.RefusedInUse, utils.SG_Refused)
		})
		reply, err := claimControl(conn, g, "tok-en-12345678", "tcp", utils.SG_ChanV2, silentLogger())
		if err != nil || reply.signal != utils.SG_Refused {
			t.Fatalf("refusal: %+v, %v", reply, err)
		}
	}
	if g.UseLegacy() || g.Strict() {
		t.Fatal("refusals changed what the client believes about the server")
	}
}
