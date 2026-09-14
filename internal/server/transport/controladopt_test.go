package transport

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/utils"
)

// The report this exists for: a tunnel that ran for three weeks, then began
// refusing its own client with "the server already has a control channel from
// somebody else" — on a tunnel that has exactly one client.
//
// Nothing on the server side reads the control channel with a deadline, and a
// heartbeat written into a dead-but-unreset socket lands in the send buffer and
// reports success. So the server can hold a control channel whose peer has been
// gone for a long time. The client does keep a read deadline, so it notices in
// seconds and re-dials — into a refusal it can do nothing about, for as long as
// the stale socket survives.
//
// The udp, kcp, quic and websocket transports already adopt such a claim by
// rebuilding the run. tcp and tcpmux refused it. These pin the two that were
// left out.

// pipePair returns the two ends of an in-memory connection. Nothing here needs
// a real address, and a pipe fails its next read the moment it is closed, which
// is how these tests see a connection being let go.
func pipePair(t *testing.T) (server, client net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

// drain reads and discards until the far end goes away, so a handshake writing
// into a pipe never blocks on nobody reading it.
func drain(conn net.Conn) {
	go func() {
		buf := make([]byte, 256)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()
}

// closedSoon reports whether conn was closed within a short wait.
func closedSoon(t *testing.T, conn net.Conn) bool {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var b [1]byte
		_, err := conn.Read(b[:])
		done <- err
	}()
	select {
	case err := <-done:
		return err != nil
	case <-time.After(3 * time.Second):
		return false
	}
}

// adoptTestTransport builds a tcp transport whose parent context has already
// finished.
//
// The adopting path asks for a Restart, and a Restart on a finished parent
// abandons itself rather than binding listeners — which is what a unit test
// wants. newTestTransport cannot be used here: it leaves parentctx nil, and
// Restart reaches parentctx.Err(), which panics on a nil context inside a
// goroutine and takes the test binary with it.
func adoptTestTransport(token string) (*TcpTransport, *tcpGen, context.Context) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := &TcpTransport{
		config:    &TcpConfig{Token: token, ChannelSize: 1},
		parentctx: ctx,
		logger:    quietLogger(),
	}
	s.run.set(ctx, func() {})

	return s, &tcpGen{ctx: ctx, handshakeChannel: make(chan controlCandidate, 1)}, ctx
}

// A second claim carrying the right token adopts the tunnel rather than being
// refused.
func TestTcpAdoptsANewControlClaimRatherThanRefusingIt(t *testing.T) {
	const token = "a-token-both-ends-share"
	s, g, _ := adoptTestTransport(token)

	// A control channel is already established — the stale one.
	stale, _ := pipePair(t)
	s.controlChannel.Set(stale)

	srv, cli := pipePair(t)
	drain(cli)

	done := make(chan struct{})
	go func() {
		s.admitControlChannel(g, srv, announcement{signal: utils.SG_Chan, payload: token})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("admitControlChannel did not return")
	}

	if len(g.handshakeChannel) != 0 {
		t.Error("the new claim was published as this run's control channel; it has to " +
			"rebuild the run instead, or two channels exist at once")
	}
	if !closedSoon(t, srv) {
		t.Error("the adopting path left the claim's connection open")
	}
}

// The token is what makes adoption safe. A claim that cannot present it is
// refused and must not disturb the tunnel — otherwise anything able to reach
// the port could knock it over.
func TestAClaimWithTheWrongTokenCannotDisturbTheTunnel(t *testing.T) {
	const token = "a-token-both-ends-share"
	s, g, _ := adoptTestTransport(token)

	stale, _ := pipePair(t)
	s.controlChannel.Set(stale)

	srv, cli := pipePair(t)
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 256)
		n, err := cli.Read(buf)
		if err != nil {
			got <- ""
			return
		}
		got <- string(buf[:n])
	}()

	s.admitControlChannel(g, srv, announcement{signal: utils.SG_Chan, payload: "the-wrong-token"})

	select {
	case answer := <-got:
		if !strings.Contains(answer, utils.RefusedBadToken) {
			t.Errorf("a wrong token was not refused as one; the claimant was told %q", answer)
		}
	case <-time.After(3 * time.Second):
		t.Error("a wrong token got no answer at all, which reads to a client exactly " +
			"like an old server")
	}

	if s.controlChannel.Get() != stale {
		t.Error("a claim with the wrong token replaced the established control channel")
	}
}

// The order is the whole security property: the token is checked before
// anything about the established channel is touched.
func TestTheTokenIsCheckedBeforeTheTunnelIsDisturbed(t *testing.T) {
	for _, f := range []string{"tcp.go", "tcpmux.go"} {
		t.Run(f, func(t *testing.T) {
			body := withoutComments(funcSourceOf(t, readTransportSource(t, f), ") admitControlChannel("))

			tok := strings.Index(body, "tokenMatches(")
			adopt := strings.Index(body, "s.controlChannel.IsSet()")
			if tok < 0 {
				t.Fatal("admitControlChannel no longer checks the token")
			}
			if adopt < 0 {
				t.Fatal("admitControlChannel no longer adopts a second claim")
			}
			if adopt < tok {
				t.Error("the established channel is disturbed before the token is checked, " +
					"so anything that can reach the port can knock the tunnel over")
			}
			if strings.Contains(body, "RefusedInUse") {
				t.Error("a second claim is still refused; one client whose path died " +
					"silently can never get back in")
			}
		})
	}
}

// funcSourceOf returns one function's body from a file's source, and fails the
// test when it cannot find it.
//
// Assertions about one function are made against that function rather than the
// whole file: a search over a file cannot tell an order inside one function
// from the order of two unrelated ones, and order is the point here. It fails
// loudly rather than returning "", because a guard that quietly matches nothing
// passes every check while testing nothing at all.
// withoutComments drops line comments from Go source.
//
// These guards assert on what the code does, and a comment explaining what the
// code used to do is not what it does. The passage above the adopting path
// names RefusedInUse to say why refusing was wrong, and a search over the raw
// body matched that word and failed the guard against correct code. Fixing the
// guard is the right way round: the comment earns its place, the search did not
// deserve to see it.
//
// A line-comment strip, not a Go parser: it would also cut a "//" inside a
// string literal, and the two function bodies these guards read contain none.
func withoutComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func funcSourceOf(t *testing.T, src, marker string) string {
	t.Helper()
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("%q is not in the source any more; this guard needs updating", marker)
	}
	rest := src[i:]
	j := strings.Index(rest, "\n}")
	if j < 0 {
		t.Fatalf("%q is never closed; this guard needs updating", marker)
	}
	return rest[:j]
}
