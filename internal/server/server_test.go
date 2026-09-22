package server

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/utils"
)

// startTransport on this side is the same shape of code as on the client: a
// long copy from one config into another, once per transport, where a field
// that is added to one and not the other is a setting that does nothing and
// says nothing.
//
// This package had no test file at all.

func testServer(cfg *config.ServerConfig) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{config: cfg, ctx: ctx, cancel: cancel, logger: utils.NewLogger("fatal")}
}

// freePort takes a port the kernel is not using, so two tests never fight over
// one — the transports here really do bind.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func baseConfig(t *testing.T, tr config.TransportType) *config.ServerConfig {
	t.Helper()
	return &config.ServerConfig{
		BindAddr:  fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		Transport: tr, Token: "t", ChannelSize: 64, Keepalive: 75,
		Heartbeat: 20, MuxCon: 8, MuxVersion: 2, MaxFrameSize: 32768,
		MaxReceiveBuffer: 1 << 20, MaxStreamBuffer: 65536,
		LogLevel: "fatal", SkipOptz: true,
		Ports: []string{fmt.Sprintf("%d=127.0.0.1:9", freePort(t))},
	}
}

// A name that falls through the switch reaches logger.Fatal, which kills the
// process. A gap here is not a wrong answer; it is a tunnel that will not
// start.
func TestEveryTransportStarts(t *testing.T) {
	want := map[config.TransportType]string{
		config.TCP:     "*transport.TcpTransport",
		config.STEALTH: "*transport.TcpTransport",
		config.TCPMUX:  "*transport.TcpMuxTransport",
		config.KCP:     "*transport.KcpTransport",
		config.QUIC:    "*transport.QuicTransport",
		config.WS:      "*transport.WsTransport",
		config.WSMUX:   "*transport.WsMuxTransport",
		config.UDP:     "*transport.UdpTransport",
	}
	for tr, typeName := range want {
		t.Run(string(tr), func(t *testing.T) {
			s := testServer(baseConfig(t, tr))
			defer s.Stop()
			ctx, cancel := context.WithCancel(s.ctx)
			defer cancel()

			r := s.startTransport(ctx, tr)
			if r == nil {
				t.Fatalf("%s produced no transport", tr)
			}
			if got := reflect.TypeOf(r).String(); got != typeName {
				t.Fatalf("%s produced %s, want %s", tr, got, typeName)
			}
			if r.Running() {
				t.Fatalf("%s reported a paired client before one had connected", tr)
			}
		})
	}
}

// Cancelling a transport's context has to release its ports, or the fallback
// chain cannot exist: the next candidate binds the same forwarded ports, and
// a candidate that has not let go means the one after it never starts.
func TestCancellingATransportReleasesItsPorts(t *testing.T) {
	cfg := baseConfig(t, config.TCP)
	s := testServer(cfg)
	defer s.Stop()

	ctx, cancel := context.WithCancel(s.ctx)
	s.startTransport(ctx, config.TCP)

	// Wait for it to actually be listening, rather than assuming.
	bind := cfg.BindAddr
	if !eventually(3*time.Second, func() bool { return dialable(bind) }) {
		t.Fatalf("the transport never bound %s", bind)
	}

	cancel()

	// And then let go of it. The chain gives a candidate a few seconds for
	// exactly this; anything longer than that here would mean the chain's
	// teardown window is too short.
	if !eventually(5*time.Second, func() bool { return !dialable(bind) }) {
		t.Fatalf("%s was still held after the transport's context was cancelled", bind)
	}
}

// Two transports started in sequence on the same ports must both work, which
// is the whole mechanism behind the fallback chain on this end.
func TestASecondTransportCanTakeTheSamePorts(t *testing.T) {
	cfg := baseConfig(t, config.TCP)
	s := testServer(cfg)
	defer s.Stop()

	first, cancelFirst := context.WithCancel(s.ctx)
	s.startTransport(first, config.TCP)
	if !eventually(3*time.Second, func() bool { return dialable(cfg.BindAddr) }) {
		t.Fatal("the first transport never bound")
	}
	cancelFirst()
	if !eventually(5*time.Second, func() bool { return !dialable(cfg.BindAddr) }) {
		t.Fatal("the first transport never let go")
	}

	second, cancelSecond := context.WithCancel(s.ctx)
	defer cancelSecond()
	s.startTransport(second, config.TCP)
	if !eventually(3*time.Second, func() bool { return dialable(cfg.BindAddr) }) {
		t.Fatal("the second transport could not take the port the first had released")
	}
}

// Stopping the server has to return, not hold the process open.
func TestStartReturnsWhenTheServerIsStopped(t *testing.T) {
	s := testServer(baseConfig(t, config.TCP))
	done := make(chan struct{})
	go func() { defer close(done); s.Start() }()
	time.Sleep(50 * time.Millisecond)
	s.Stop()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
}

func dialable(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func eventually(limit time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

// Start has to mean stopped.
//
// It returned when the transport's supervisor stopped, not when the goroutines
// it launched had — so for a window afterwards the old run still held its
// ports. Production papers over it: Restart sleeps two seconds for exactly
// this, and the comment there says so.
//
// It is not cosmetic. A reload builds the next generation as soon as the
// previous Start returns, so the two fight for the same ports and the sleep is
// only *likely* to win. It also made a CI test flake, which is how it was
// found.
func TestStartDoesNotReturnUntilThePortsAreFree(t *testing.T) {
	for _, tr := range []config.TransportType{
		config.TCP, config.TCPMUX, config.WS, config.WSMUX,
		config.KCP, config.QUIC, config.UDP,
	} {
		t.Run(string(tr), func(t *testing.T) {
			cfg := baseConfig(t, tr)
			s := testServer(cfg)

			done := make(chan struct{})
			go func() { defer close(done); s.Start() }()

			// Wait until it is actually listening, rather than assuming.
			if !eventually(5*time.Second, func() bool { return bound(cfg.BindAddr, tr) }) {
				t.Fatalf("%s never bound %s", tr, cfg.BindAddr)
			}

			s.Stop()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatalf("%s: Start did not return", tr)
			}

			// The moment Start returns, the port must be takeable. No polling,
			// no grace: that is the whole claim.
			if bound(cfg.BindAddr, tr) {
				t.Fatalf("%s: Start returned while %s was still held — a reload "+
					"starting the next generation here fights this one for its own "+
					"ports", tr, cfg.BindAddr)
			}
		})
	}
}

// bound reports whether something is holding addr, on whichever protocol this
// transport listens with.
//
// udp is in both lists on purpose and it is not a mistake in the table: its
// *data* is UDP and its *control channel* is TCP, so it holds both and either
// one still being held is a port the next generation cannot take.
func bound(addr string, tr config.TransportType) bool {
	switch tr {
	case config.KCP, config.QUIC:
		return udpBound(addr)
	case config.UDP:
		return udpBound(addr) || tcpBound(addr)
	}
	return tcpBound(addr)
}

func tcpBound(addr string) bool {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return true
	}
	l.Close()
	return false
}

func udpBound(addr string) bool {
	c, err := net.ListenPacket("udp", addr)
	if err != nil {
		return true
	}
	c.Close()
	return false
}
