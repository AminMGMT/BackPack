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
