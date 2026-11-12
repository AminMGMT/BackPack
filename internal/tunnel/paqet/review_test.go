package paqet

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
)

// A hello that goes unanswered burns its source port, so the next attempt goes
// out as a different flow. The socket's close used to release the port before
// it was burned, and every retry was sent from the same port.
func TestEveryUnansweredAttemptGoesOutFromANewSourcePort(t *testing.T) {
	oldHello := helloTimeout
	helloTimeout = 200 * time.Millisecond
	defer func() { helloTimeout = oldHello }()

	p := newPair(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.client.Run(ctx) }() // nobody answers
	deadline := time.Now().Add(15 * time.Second)
	for len(p.cLease.history()) < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	h := p.cLease.history()
	if len(h) < 3 {
		t.Fatalf("only %d attempts were made", len(h))
	}
	seen := map[uint16]bool{}
	for _, port := range h[:3] {
		if seen[port] {
			t.Fatalf("attempts reused a source port: %v", h)
		}
		seen[port] = true
	}
	if n := p.cLease.inUse(); n != 0 {
		t.Fatalf("%d ports still claimed after the generation ended", n)
	}
}

// kcp-go's listener leaves the sessions it accepted open when it closes, so a
// server generation that ended kept them flushing to a closed wire.
func TestAServerGenerationThatEndsClosesItsSessions(t *testing.T) {
	p := newPair(t, nil)
	p.start(t)
	p.waitUp(t)
	p.server.mu.RLock()
	g := p.server.gen
	p.server.mu.RUnlock()
	g.mu.RLock()
	sessions := append([]*session(nil), g.sessions...)
	g.mu.RUnlock()
	if len(sessions) == 0 {
		t.Fatal("the server has no session")
	}
	p.stop()
	// Well inside the 2 s keepalive timeout the test pair runs with: the
	// sessions are closed by the generation, not timed out by smux.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		open := 0
		for _, s := range sessions {
			if !s.sm.IsClosed() {
				open++
			}
		}
		if open == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the server's sessions outlived its generation")
}

// rawSession opens a KCP+smux session to the pair's server without the
// engine's client, so a test can say what a client that skips steps sends.
func rawSession(t *testing.T, p *pair) *smux.Session {
	t.Helper()
	w := NewWire(p.cEnd, p.cEnd, p.cEnd.ip)
	t.Cleanup(func() { w.Close() })
	sock, err := w.Open(41000, nil)
	if err != nil {
		t.Fatal(err)
	}
	kc, err := kcp.NewConn("10.0.0.2:9443", p.server.block, 0, 0, sock)
	if err != nil {
		t.Fatal(err)
	}
	applyKCP(kc, p.server.p)
	sm, err := smux.Client(kc, smuxConfig(p.server.p))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sm.Close(); kc.Close() })
	return sm
}

func askFor(t *testing.T, sm *smux.Session, kind byte, target string) byte {
	t.Helper()
	st, err := sm.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, _ := ParseAddr(target)
	if err := (&proto{Type: kind, Addr: a}).write(st); err != nil {
		t.Fatal(err)
	}
	st.SetReadDeadline(time.Now().Add(10 * time.Second))
	var b [1]byte
	if _, err := io.ReadFull(st, b[:]); err != nil {
		t.Fatalf("no verdict: %v", err)
	}
	return b[0]
}

// The server dials nothing for a session that never said which revision it
// speaks: a client of this version always says so first.
func TestARequestOnASessionWithoutAHelloIsRefused(t *testing.T) {
	p := newPair(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.server.Run(ctx)
	echo := tcpEcho(t)
	sm := rawSession(t, p)
	if got := askFor(t, sm, pTCP, echo.Addr().String()); got != statusRefused {
		t.Fatalf("a request without a hello got status %d, want a refusal", got)
	}
	// With the hello, the same request is served.
	if err := p.server.clientHello(sm); err != nil {
		t.Fatal(err)
	}
	if got := askFor(t, sm, pTCP, echo.Addr().String()); got != statusOK {
		t.Fatalf("after the hello the request got status %d", got)
	}
}

// By default the kharej dials only itself: the authenticated client of the
// old TUN-based PCK reached that host's services and nothing else.
func TestTheServerDialsOnlyItsOwnServicesUnlessToldOtherwise(t *testing.T) {
	p := newPair(t, nil)
	p.start(t)
	p.waitUp(t)
	reported := make(chan error, 1)
	// TEST-NET-1: never this host.
	conn, err := p.client.Dial(context.Background(), "tcp", "192.0.2.1:80", func(err error) { reported <- err })
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("a target elsewhere was connected")
	}
	var se statusError
	if err := <-reported; !errors.As(err, &se) || byte(se) != statusForbidden {
		t.Fatalf("reported %v, want the forbidden-target status", err)
	} else if !strings.Contains(err.Error(), "pck_remote_targets") {
		t.Fatalf("the message does not say how to allow it: %v", err)
	}
	// UDP is held to the same rule.
	uc, err := p.client.Dial(context.Background(), "udp", "192.0.2.1:53", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer uc.Close()
	uc.Write([]byte("q"))
	uc.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := uc.Read(make([]byte, 16)); !errors.As(err, &se) || byte(se) != statusForbidden {
		t.Fatalf("udp to elsewhere: %v", err)
	}
}

func TestTheTargetPolicy(t *testing.T) {
	own := net.ParseIP("203.0.113.7")
	pol := &targetPolicy{
		addrs: func() ([]net.Addr, error) {
			return []net.Addr{&net.IPNet{IP: own, Mask: net.CIDRMask(24, 32)}}, nil
		},
		lookup: func(_ context.Context, host string) ([]net.IP, error) {
			switch host {
			case "localhost":
				return []net.IP{net.ParseIP("::1"), net.ParseIP("127.0.0.1")}, nil
			case "elsewhere.example":
				return []net.IP{net.ParseIP("198.51.100.1")}, nil
			}
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		},
	}
	ctx := context.Background()
	for _, c := range []struct {
		in, want string
	}{
		{"127.0.0.1:8443", "127.0.0.1:8443"},
		{"127.0.0.53:53", "127.0.0.53:53"},
		{"[::1]:22", "[::1]:22"},
		{"0.0.0.0:80", "0.0.0.0:80"},
		{"203.0.113.7:443", "203.0.113.7:443"}, // the kharej's own public address
		{"localhost:80", "[::1]:80"},
	} {
		a, _ := ParseAddr(c.in)
		got, err := pol.check(ctx, a)
		if err != nil || got.String() != c.want {
			t.Errorf("%s -> %v, %v; want %s", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"169.254.169.254:80", "10.0.0.5:443", "1.1.1.1:443", "elsewhere.example:443"} {
		a, _ := ParseAddr(in)
		if _, err := pol.check(ctx, a); !errors.Is(err, errForbiddenTarget) {
			t.Errorf("%s: %v, want it refused", in, err)
		} else if statusFor(err) != statusForbidden {
			t.Errorf("%s: status %d", in, statusFor(err))
		}
	}
	a, _ := ParseAddr("nowhere.example:1")
	if _, err := pol.check(ctx, a); statusFor(err) != statusBadTarget {
		t.Errorf("an unresolvable name: %v", err)
	}
	pol.remote = true
	a, _ = ParseAddr("10.0.0.5:443")
	if got, err := pol.check(ctx, a); err != nil || got != a {
		t.Errorf("with remote targets on: %v, %v", got, err)
	}
}

func TestTheServerRefusesMoreConnectionsThanItsBound(t *testing.T) {
	p := newPair(t, nil)
	p.server.dials.max = 1
	p.start(t)
	p.waitUp(t)
	echo := tcpEcho(t)
	first, err := p.client.Dial(context.Background(), "tcp", echo.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	first.Write([]byte("x"))
	first.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(first, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	second, err := p.client.Dial(context.Background(), "tcp", echo.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	second.SetReadDeadline(time.Now().Add(10 * time.Second))
	var se statusError
	if _, err := second.Read(make([]byte, 1)); !errors.As(err, &se) || byte(se) != statusBusy {
		t.Fatalf("the connection over the bound: %v, want busy", err)
	}
	second.Close()
	first.Close()
	// The slot comes back when the first one ends.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c, err := p.client.Dial(context.Background(), "tcp", echo.Addr().String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		c.Write([]byte("y"))
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err = io.ReadFull(c, make([]byte, 1))
		c.Close()
		if err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the bound was not given back when a connection ended")
}

func TestAStreamBufferLargerThanTheSessionBufferIsRefused(t *testing.T) {
	p, _ := PresetParams(PresetTurbo)
	p = p.Resolve("")
	p.StreamBuf = p.SmuxBuf * 2
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "stream buffer") {
		t.Fatalf("Validate = %v", err)
	}
	// Every preset satisfies smux's own check.
	for _, name := range Presets() {
		pp, _ := PresetParams(name)
		if err := smux.VerifyConfig(smuxConfig(pp.Resolve(name))); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAPolicyRefusalIsToldApartFromATargetThatIsDown(t *testing.T) {
	if !RefusedByPolicy(statusError(statusForbidden)) {
		t.Fatal("the forbidden status was not recognised")
	}
	for _, err := range []error{statusError(statusRefused), statusError(statusTimeout), errors.New("x"), nil} {
		if RefusedByPolicy(err) {
			t.Fatalf("%v read as a policy refusal", err)
		}
	}
}
