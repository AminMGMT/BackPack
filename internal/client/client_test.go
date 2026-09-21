package client

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/client/transport"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"
)

// startTransport is two hundred lines of copying fields from a config into a
// transport's own config, once per transport. A field that is added to one and
// not the other produces a setting that appears in the wizard, is written to
// the file, and does nothing — with no build error and nothing failing.
//
// This package had no test file at all.

func testClient(cfg *config.ClientConfig) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{config: cfg, ctx: ctx, cancel: cancel, logger: utils.NewLogger("fatal")}
}

func baseConfig(tr config.TransportType) *config.ClientConfig {
	return &config.ClientConfig{
		RemoteAddr: "127.0.0.1:1", Transport: tr, Token: "t",
		ConnectionPool: 4, RetryInterval: 1, Keepalive: 8, DialTimeout: 5,
		MuxVersion: 2, MaxFrameSize: 32768, MaxReceiveBuffer: 1 << 20,
		MaxStreamBuffer: 65536, LogLevel: "fatal",
	}
}

// Every transport the reverse tunnel can be configured with has to produce a
// running transport. A name that falls through the switch reaches
// logger.Fatal, which kills the process — so a gap here is not a wrong answer,
// it is a tunnel that will not start.
func TestEveryTransportStarts(t *testing.T) {
	want := map[config.TransportType]string{
		config.TCP:     "*transport.TcpTransport",
		config.STEALTH: "*transport.TcpTransport",
		config.TCPMUX:  "*transport.TcpMuxTransport",
		config.KCP:     "*transport.KcpTransport",
		config.XDI:     "*transport.KcpTransport",
		config.PCK:     "*transport.KcpTransport",
		config.QUIC:    "*transport.QuicTransport",
		config.WS:      "*transport.WsTransport",
		config.WSS:     "*transport.WsTransport",
		config.WSMUX:   "*transport.WsMuxTransport",
		config.WSSMUX:  "*transport.WsMuxTransport",
		config.UDP:     "*transport.UdpTransport",
	}
	for tr, typeName := range want {
		t.Run(string(tr), func(t *testing.T) {
			c := testClient(baseConfig(tr))
			defer c.Stop()
			ctx, cancel := context.WithCancel(c.ctx)
			defer cancel()

			r := c.startTransport(ctx, tr, network.NewEndpoints("127.0.0.1:1"), nil)
			if r == nil {
				t.Fatalf("%s produced no transport", tr)
			}
			if got := reflect.TypeOf(r).String(); got != typeName {
				t.Fatalf("%s produced %s, want %s", tr, got, typeName)
			}
			// It must also answer the question a fallback chain asks, or the
			// chain would sit on a carrier it can never confirm.
			if r.Running() {
				t.Fatalf("%s reported itself connected before it had dialled", tr)
			}
		})
	}
}

// The three carriers that share the KCP transport are told apart by two flags,
// and getting either wrong sends the packets out over the wrong thing
// entirely.
func TestTheKcpCarriersAreDistinguished(t *testing.T) {
	for tr, want := range map[config.TransportType][2]bool{
		config.KCP: {false, false}, // plain UDP
		config.XDI: {true, false},  // ICMP echo
		config.PCK: {false, true},  // raw packet socket
	} {
		c := testClient(baseConfig(tr))
		defer c.Stop()
		r := c.startTransport(c.ctx, tr, network.NewEndpoints("127.0.0.1:1"), nil)
		kcp, ok := r.(*transport.KcpTransport)
		if !ok {
			t.Fatalf("%s did not produce the KCP transport", tr)
		}
		icmp, pck := kcpFlags(kcp)
		if icmp != want[0] || pck != want[1] {
			t.Fatalf("%s: useICMP=%v usePck=%v, want %v", tr, icmp, pck, want)
		}
	}
}

// An invalid outbound configuration must not stop the tunnel. It was validated
// at load, so reaching here means the file changed underneath us, and dialling
// directly is the safe reading.
func TestABadOutboundFallsBackToDiallingDirectly(t *testing.T) {
	cfg := baseConfig(config.TCP)
	cfg.Proxy = "://nonsense"
	if _, err := buildOutbound(cfg); err == nil {
		t.Fatal("an unparseable proxy was accepted")
	}
}

// A config with none of the outbound settings costs nothing to carry, and must
// produce nil rather than an empty struct the dialler would then honour.
func TestNoOutboundSettingsProduceNothing(t *testing.T) {
	out, err := buildOutbound(baseConfig(config.TCP))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != nil {
		t.Fatalf("an unconfigured outbound produced %+v", out)
	}
}

func TestOutboundSettingsAreCarried(t *testing.T) {
	cfg := baseConfig(config.TCP)
	// Loopback, because Validate checks the interface actually exists on this
	// machine — which is itself worth knowing: a typo in the config is caught
	// at load rather than at the first dial.
	cfg.Interface = "lo"
	cfg.SOMark = 42
	out, err := buildOutbound(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil || out.Interface != "lo" || out.Mark != 42 {
		t.Fatalf("outbound = %+v", out)
	}
}

// A named interface that is not on this machine is a typo, and it is better
// caught now than at the first dial.
func TestAnInterfaceThatDoesNotExistIsRefused(t *testing.T) {
	cfg := baseConfig(config.TCP)
	cfg.Interface = "no-such-interface0"
	if _, err := buildOutbound(cfg); err == nil {
		t.Fatal("an interface that does not exist was accepted")
	}
}

// Cancelling the client's context is how everything stops. Start must return
// rather than hold the process open.
func TestStartReturnsWhenTheClientIsStopped(t *testing.T) {
	c := testClient(baseConfig(config.TCP))
	done := make(chan struct{})
	go func() { defer close(done); c.Start() }()
	time.Sleep(50 * time.Millisecond)
	c.Stop()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
}

// kcpFlags reads the two carrier flags back off a started transport. They are
// unexported in the transport package, so this goes through reflection rather
// than widening that package's surface for a test.
func kcpFlags(k *transport.KcpTransport) (useICMP, usePck bool) {
	v := reflect.ValueOf(k).Elem().FieldByName("config").Elem()
	return v.FieldByName("UseICMP").Bool(), v.FieldByName("UsePck").Bool()
}
