package transport

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Behaviour on the server transports that can be exercised without a tunnel.
//
// The loops that accept and pair connections are proved end to end; what is
// held still here is everything they decide with. Two of these — the bind
// failure message and sameHost — are the kind of code that is only ever read
// when something has already gone wrong, so being wrong in them is expensive
// and silent.

// A bind failure is read by an operator who is usually looking at the wrong
// server. Every word of it has to earn its place.
func TestABindFailureSaysWhichProblemItIs(t *testing.T) {
	inUse := bindFailure("tunnel port", "0.0.0.0:443", syscall.EADDRINUSE)
	if !strings.Contains(inUse, "THIS server") {
		t.Fatalf("an in-use message does not say where to look:\n%s", inUse)
	}
	// The one thing the log cannot know is which process holds the port, and
	// the one thing the operator can find in a second is exactly that.
	if !strings.Contains(inUse, "ss -tlnp") || !strings.Contains(inUse, "443") {
		t.Fatalf("an in-use message does not name the command that finds the culprit:\n%s", inUse)
	}

	notAvail := bindFailure("tunnel port", "85.10.11.51:443", syscall.EADDRNOTAVAIL)
	if notAvail == inUse {
		t.Fatal("two completely different failures produced the same message")
	}
	// This is the failure a pinned control address introduces: the kernel does
	// not have the address, which is a different fix from a port collision.
	if !strings.Contains(notAvail, "85.10.11.51") {
		t.Fatalf("an unavailable-address message does not name the address:\n%s", notAvail)
	}
}

// A failure that is neither must still produce something, rather than an empty
// line where an explanation belongs.
func TestAnUnrecognisedBindFailureStillExplainsItself(t *testing.T) {
	msg := bindFailure("tunnel port", "0.0.0.0:443", errors.New("something else entirely"))
	if strings.TrimSpace(msg) == "" {
		t.Fatal("an unrecognised bind failure produced nothing")
	}
	if !strings.Contains(msg, "0.0.0.0:443") {
		t.Fatalf("it does not name the address:\n%s", msg)
	}
}

// The errno tests have to see through the wrappers the net package puts round
// them, or every real failure falls through to the generic message.
func TestBindErrorsAreRecognisedThroughWrapping(t *testing.T) {
	wrapped := &net.OpError{Op: "listen", Net: "tcp",
		Err: &net.AddrError{Err: "x", Addr: "y"}}
	if isAddrInUse(wrapped) || isAddrNotAvail(wrapped) {
		t.Fatal("an unrelated error was taken for a bind errno")
	}
	real := &net.OpError{Op: "listen", Net: "tcp",
		Err: &net.OpError{Op: "bind", Err: syscall.EADDRINUSE}}
	if !isAddrInUse(real) {
		t.Fatal("EADDRINUSE was not recognised through the net package's wrappers")
	}
	notAvail := &net.OpError{Op: "listen", Err: syscall.EADDRNOTAVAIL}
	if !isAddrNotAvail(notAvail) {
		t.Fatal("EADDRNOTAVAIL was not recognised through the net package's wrappers")
	}
}

func TestPortOfHandlesEveryShapeOfAddress(t *testing.T) {
	for in, want := range map[string]string{
		":62050":            "62050",
		"1.2.3.4:62050":     "62050",
		"[::1]:62050":       "62050",
		"[2a01:4f8::1]:443": "443",
		// Not an address at all: handed back whole, which still makes a usable
		// grep rather than an empty one.
		"nonsense": "nonsense",
	} {
		if got := portOf(in); got != want {
			t.Fatalf("portOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// The tunnel port is not optional: a failed bind there cannot be skipped, and
// exiting only means the supervisor restarts into the same failure for ever.
// The backoff is what makes waiting correct instead.
func TestTheListenBackoffGrowsAndIsCapped(t *testing.T) {
	var b listenBackoff
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately: the growth is the behaviour under test, not the
	// waiting, and a test that really waited would take a minute.
	cancel()

	var delays []time.Duration
	for i := 0; i < 8; i++ {
		b.wait(ctx)
		delays = append(delays, b.delay)
	}
	if delays[0] != listenRetryFirst {
		t.Fatalf("first delay = %v, want %v", delays[0], listenRetryFirst)
	}
	for i := 1; i < len(delays); i++ {
		if delays[i] < delays[i-1] {
			t.Fatalf("the backoff went backwards: %v", delays)
		}
		if delays[i] > listenRetryMax {
			t.Fatalf("the backoff passed its cap: %v", delays)
		}
	}
	if delays[len(delays)-1] != listenRetryMax {
		t.Fatalf("the backoff never reached its cap: %v", delays)
	}
}

// A run that ended while the backoff was waiting must stop, not bind a port for
// a tunnel that is going away — on a reload that would mean fighting the run
// replacing it for its own ports.
func TestTheListenBackoffStopsWhenTheRunEnds(t *testing.T) {
	var b listenBackoff
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if b.wait(ctx) {
		t.Fatal("the backoff carried on after the run ended")
	}
}

// sameHost decides whether a connection is from the peer that holds the
// control channel. Comparing strings would make the same address written two
// ways look like two different hosts.
func TestSameHostComparesAddressesNotStrings(t *testing.T) {
	addr := func(s string) net.Addr {
		a, err := net.ResolveTCPAddr("tcp", s)
		if err != nil {
			t.Fatalf("resolving %q: %v", s, err)
		}
		return a
	}
	same := [][2]string{
		{"1.2.3.4:100", "1.2.3.4:200"},          // same host, different ports
		{"[::1]:100", "[0:0:0:0:0:0:0:1]:200"},  // the same IPv6 address, written twice
		{"[::ffff:1.2.3.4]:100", "1.2.3.4:200"}, // IPv4-mapped IPv6
	}
	for _, c := range same {
		if !sameHost(addr(c[0]), addr(c[1])) {
			t.Fatalf("%q and %q were not recognised as the same host", c[0], c[1])
		}
	}
	if sameHost(addr("1.2.3.4:100"), addr("1.2.3.5:100")) {
		t.Fatal("two different hosts matched")
	}
	// A nil address never matches anything: "we do not know who this is" must
	// not be mistaken for "this is the peer".
	if sameHost(nil, addr("1.2.3.4:100")) || sameHost(addr("1.2.3.4:100"), nil) {
		t.Fatal("a nil address matched")
	}
}

// A listener with no certificate gets one generated for it, and the name in it
// has to be something a client can actually be pointed at.
func TestCertHostNamesTheBindAddress(t *testing.T) {
	for in, want := range map[string]string{
		"1.2.3.4:443": "1.2.3.4",
		// Bound to everything, so it has no particular name of its own.
		"0.0.0.0:443": "localhost",
		"[::]:443":    "localhost",
		":443":        "localhost",
		"nonsense":    "localhost",
	} {
		if got := certHost(in); got != want {
			t.Fatalf("certHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// netControl holds the control channel across a generation swap. Reading it
// while the next generation replaces it is the ordinary case, not the exotic
// one.
func TestNetControlIsSafeAcrossAGenerationSwap(t *testing.T) {
	var c netControl
	if c.IsSet() {
		t.Fatal("an empty holder reported a control channel")
	}
	if c.RemoteAddr() != nil {
		t.Fatal("an empty holder reported a peer address")
	}
	// Closing nothing must be safe: Restart closes the control channel
	// unconditionally, including on a run that never got one.
	c.Close()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				_ = c.IsSet()
				_ = c.RemoteAddr()
				_ = c.Get()
			}
		}()
	}
	for i := 0; i < 500; i++ {
		c.Set(nil)
		c.Clear()
	}
	wg.Wait()
}

func TestWSControlIsSafeAcrossAGenerationSwap(t *testing.T) {
	var c wsControl
	if c.IsSet() {
		t.Fatal("an empty holder reported a control channel")
	}
	c.Close()
	c.Clear()
	if c.Get() != nil {
		t.Fatal("clearing left something behind")
	}
}

// Running() is what a fallback chain asks of the server end, and it has to
// mean "a client has paired with me".
func TestEveryServerTransportAnswersRunning(t *testing.T) {
	type probe struct {
		name string
		set  func(string)
		run  func() bool
	}
	tcp := &TcpTransport{}
	mux := &TcpMuxTransport{}
	kcp := &KcpTransport{}
	q := &QuicTransport{}
	ws := &WsTransport{}
	wsm := &WsMuxTransport{}
	udp := &UdpTransport{}
	each := []probe{
		{"tcp", tcp.status.set, tcp.Running},
		{"tcpmux", mux.status.set, mux.Running},
		{"kcp", kcp.status.set, kcp.Running},
		{"quic", q.status.set, q.Running},
		{"ws", ws.status.set, ws.Running},
		{"wsmux", wsm.status.set, wsm.Running},
		{"udp", udp.status.set, udp.Running},
	}
	if len(each) != 7 {
		t.Fatalf("checked %d transports, the package has 7", len(each))
	}
	for _, e := range each {
		if e.run() {
			t.Fatalf("%s reported paired before anything happened", e.name)
		}
		// A cleared status is what Restart leaves behind, and it must not read
		// as still connected — the chain would then hold a carrier that has
		// nothing on it.
		e.set("")
		if e.run() {
			t.Fatalf("%s reported paired on a cleared status", e.name)
		}
		e.set("Connected (x)")
		if !e.run() {
			t.Fatalf("%s did not report paired when connected", e.name)
		}
		e.set("Disconnected (x)")
		if e.run() {
			t.Fatalf("%s kept reporting paired after the client left", e.name)
		}
	}
}

// runState is the generation's context, replaced on every restart while the
// previous generation's goroutines are still reading it.
func TestRunStateSwapsWholeGenerations(t *testing.T) {
	var r runState
	first, cancelFirst := context.WithCancel(context.Background())
	r.set(first, cancelFirst)
	if r.context() != first {
		t.Fatal("set did not publish the context")
	}

	second, cancelSecond := context.WithCancel(context.Background())
	r.set(second, cancelSecond)
	if r.context() != second {
		t.Fatal("the second generation was not published")
	}

	// stop ends the current generation and must be safe to call again: Restart
	// and a shutdown can both reach it.
	r.stop()
	r.stop()
	if second.Err() == nil {
		t.Fatal("stop did not end the generation")
	}
	cancelFirst()
}
