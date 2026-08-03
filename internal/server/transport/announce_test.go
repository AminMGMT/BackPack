package transport

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"

	"github.com/sirupsen/logrus"
)

func quietLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

// connPair returns a connected pair of real TCP sockets on the given loopback
// address, so the connection under test has a genuine remote address — which is
// the whole point of these tests.
func connPair(t *testing.T, host string) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skipf("cannot listen on %s: %v", host, err)
	}
	defer ln.Close()

	type accepted struct {
		conn net.Conn
		err  error
	}
	ch := make(chan accepted, 1)
	go func() {
		c, err := ln.Accept()
		ch <- accepted{c, err}
	}()

	client, err = net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", ln.Addr(), err)
	}
	a := <-ch
	if a.err != nil {
		t.Fatalf("accept on %s: %v", ln.Addr(), a.err)
	}
	t.Cleanup(func() { client.Close(); a.conn.Close() })
	return client, a.conn
}

func newTestTransport(token string) (*TcpTransport, *tcpGen) {
	s := &TcpTransport{
		config: &TcpConfig{Token: token},
		logger: quietLogger(),
	}
	g := &tcpGen{
		tunnelChannel:    make(chan net.Conn, 4),
		handshakeChannel: make(chan controlCandidate, 1),
	}
	return s, g
}

// A pool connection is admitted on the nonce it presents, whatever address it
// dialled from.
//
// This is the failure the nonce exists to fix, reproduced as closely as a test
// can: the control channel is on one address and the pool connection arrives on
// a different one, which is what happens to any client that dials out through
// carrier-grade NAT, a SNAT pool or a multi-homed host. Under the old rule —
// admit only what matches the control channel's source address — this
// connection was discarded, the pool stayed empty, and the tunnel reported
// itself connected while carrying nothing.
func TestPoolConnectionAdmittedFromADifferentAddress(t *testing.T) {
	// ::1 and 127.0.0.1 are different hosts to sameHost, which is what makes
	// this a genuine reproduction rather than a restatement of the new code.
	if l, err := net.Listen("tcp", "[::1]:0"); err != nil {
		t.Skip("no IPv6 loopback on this machine")
	} else {
		l.Close()
	}

	s, g := newTestTransport("a-token")

	// The control channel is established from ::1 ...
	_, controlServerSide := connPair(t, "::1")
	nonce, err := network.NewPoolNonce()
	if err != nil {
		t.Fatalf("NewPoolNonce: %v", err)
	}
	s.poolNonce.Set(nonce)
	s.controlChannel.Set(controlServerSide)

	// ... and the pool connection arrives from 127.0.0.1.
	poolClientSide, poolServerSide := connPair(t, "127.0.0.1")
	go func() {
		_ = utils.SendBinaryTransportString(poolClientSide, nonce, utils.SG_Pool)
	}()

	done := make(chan struct{})
	go func() { defer close(done); s.admitTunnelConn(g, poolServerSide) }()

	select {
	case admitted := <-g.tunnelChannel:
		if admitted != poolServerSide {
			t.Fatalf("a different connection reached the pool")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a pool connection with a valid nonce was not admitted from a different source address")
	}
	<-done
}

// A connection that presents the wrong nonce is refused. Dropping the source
// address check must not mean admitting anything that connects.
func TestPoolConnectionWithWrongNonceRefused(t *testing.T) {
	s, g := newTestTransport("a-token")

	_, controlServerSide := connPair(t, "127.0.0.1")
	nonce, err := network.NewPoolNonce()
	if err != nil {
		t.Fatalf("NewPoolNonce: %v", err)
	}
	s.poolNonce.Set(nonce)
	s.controlChannel.Set(controlServerSide)

	wrong, _ := network.NewPoolNonce()
	poolClientSide, poolServerSide := connPair(t, "127.0.0.1")
	go func() {
		_ = utils.SendBinaryTransportString(poolClientSide, wrong, utils.SG_Pool)
	}()

	s.admitTunnelConn(g, poolServerSide)

	select {
	case <-g.tunnelChannel:
		t.Fatal("a pool connection presenting the wrong nonce was admitted")
	default:
	}
}

// A pool connection carrying the previous run's nonce is refused: Restart
// clears the nonce, and an empty one must close the door rather than open it.
func TestPoolConnectionRefusedAfterNonceCleared(t *testing.T) {
	s, g := newTestTransport("a-token")

	_, controlServerSide := connPair(t, "127.0.0.1")
	nonce, err := network.NewPoolNonce()
	if err != nil {
		t.Fatalf("NewPoolNonce: %v", err)
	}
	s.poolNonce.Set(nonce)
	s.controlChannel.Set(controlServerSide)
	s.poolNonce.Clear()
	// With the nonce gone the legacy branch takes over, so put the control
	// channel out of the way too — otherwise this tests source addresses, not
	// the nonce.
	s.controlChannel.Clear()

	poolClientSide, poolServerSide := connPair(t, "127.0.0.1")
	go func() {
		_ = utils.SendBinaryTransportString(poolClientSide, nonce, utils.SG_Pool)
	}()

	s.admitTunnelConn(g, poolServerSide)

	select {
	case <-g.tunnelChannel:
		t.Fatal("a pool connection carrying a cleared nonce was admitted")
	default:
	}
}

// A v2 control handshake is answered with the token and a nonce, and the
// candidate carries that same nonce through to channelHandshake.
func TestControlHandshakeV2IssuesANonce(t *testing.T) {
	s, g := newTestTransport("a-token")

	clientSide, serverSide := connPair(t, "127.0.0.1")
	go func() {
		_ = utils.SendBinaryTransportString(clientSide, "a-token", utils.SG_ChanV2)
	}()

	go s.admitTunnelConn(g, serverSide)

	if err := clientSide.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	ack, signal, err := utils.ReceiveBinaryTransportString(clientSide)
	if err != nil {
		t.Fatalf("no answer to the v2 handshake: %v", err)
	}
	if signal != utils.SG_ChanV2 {
		t.Fatalf("answer came back as signal %d, want SG_ChanV2", signal)
	}
	token, nonce := network.DecodeControlAck(ack)
	if token != "a-token" {
		t.Fatalf("answer carried token %q, want %q", token, "a-token")
	}
	if nonce == "" {
		t.Fatal("a v2 handshake was answered without a nonce")
	}

	select {
	case candidate := <-g.handshakeChannel:
		if candidate.nonce != nonce {
			t.Fatalf("candidate carries nonce %q, but %q was sent to the client", candidate.nonce, nonce)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the control channel candidate never reached channelHandshake")
	}
}

// A legacy control handshake is answered exactly as before — the bare token,
// under the old signal — and issues no nonce, which is what leaves the
// source-address fallback in place for a client that cannot present one.
func TestControlHandshakeLegacyIssuesNoNonce(t *testing.T) {
	s, g := newTestTransport("a-token")

	clientSide, serverSide := connPair(t, "127.0.0.1")
	go func() {
		_ = utils.SendBinaryTransportString(clientSide, "a-token", utils.SG_Chan)
	}()

	go s.admitTunnelConn(g, serverSide)

	if err := clientSide.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	ack, signal, err := utils.ReceiveBinaryTransportString(clientSide)
	if err != nil {
		t.Fatalf("no answer to the legacy handshake: %v", err)
	}
	if signal != utils.SG_Chan {
		t.Fatalf("answer came back as signal %d, want SG_Chan", signal)
	}
	if ack != "a-token" {
		t.Fatalf("answer carried %q, want the bare token", ack)
	}

	select {
	case candidate := <-g.handshakeChannel:
		if candidate.nonce != "" {
			t.Fatalf("a legacy handshake issued nonce %q", candidate.nonce)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the control channel candidate never reached channelHandshake")
	}
}

// A control handshake offering the wrong token is refused, and no candidate
// reaches channelHandshake.
func TestControlHandshakeRejectsAWrongToken(t *testing.T) {
	s, g := newTestTransport("a-token")

	clientSide, serverSide := connPair(t, "127.0.0.1")
	go func() {
		_ = utils.SendBinaryTransportString(clientSide, "not-the-token", utils.SG_ChanV2)
	}()

	s.admitTunnelConn(g, serverSide)

	select {
	case <-g.handshakeChannel:
		t.Fatal("a handshake with the wrong token produced a control channel candidate")
	default:
	}
}

// A peer that connects and then says nothing must not hold up the connections
// behind it. It used to: the announcement was read in one loop that took a
// single connection at a time, so a scanner on the tunnel port could keep the
// tunnel's own client from ever establishing a control channel.
func TestASilentPeerDoesNotBlockTheNextConnection(t *testing.T) {
	s, g := newTestTransport("a-token")

	// A peer that connects and sends nothing at all.
	_, silent := connPair(t, "127.0.0.1")
	go s.admitTunnelConn(g, silent)

	// A genuine control channel arriving right behind it.
	clientSide, serverSide := connPair(t, "127.0.0.1")
	go func() {
		_ = utils.SendBinaryTransportString(clientSide, "a-token", utils.SG_ChanV2)
	}()
	go s.admitTunnelConn(g, serverSide)

	// Well inside announceTimeout, so this can only pass if the silent peer is
	// not being waited on first.
	select {
	case <-g.handshakeChannel:
	case <-time.After(announceTimeout / 2):
		t.Fatal("a silent peer delayed the control channel behind it")
	}
}
