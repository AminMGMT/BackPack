package engine

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/client"
	"github.com/backpack/backpack/internal/forwardmap"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/server"
)

// forwardProvider is Backpack's application-level direct mode: the Iran edge
// dials Kharej using a normal Backpack transport, while user-facing ports stay
// on Iran and backends stay on Kharej. It is not the iptables DNAT provider.
type forwardProvider struct{}

func init() { Register(config.EngineForward, forwardProvider{}) }

func (forwardProvider) Metadata() Metadata { return Metadata{Name: "forward", Mode: "direct"} }

func (forwardProvider) Validate(_ context.Context, r Request) error {
	if r.Config == nil {
		return fmt.Errorf("nil forward tunnel configuration")
	}
	if err := r.Config.ValidateStructure(); err != nil {
		return err
	}
	var transport config.TransportType
	if r.Config.HasClient() {
		transport = r.Config.Client.Transport
		mappings, err := forwardmap.Expand(r.Config.Client.Ports)
		if err != nil {
			return fmt.Errorf("invalid forward ingress mappings: %w", err)
		}
		if !r.Replacing {
			network := "tcp"
			if transport == config.UDP {
				network = "udp"
			}
			for _, mapping := range mappings {
				if err := probeForwardListen(network, mapping.Listen); err != nil {
					return fmt.Errorf("forward ingress %s/%s is unavailable: %w", network, mapping.Listen, err)
				}
			}
		}
	} else {
		transport = r.Config.Server.Transport
		if !r.Replacing && transport != config.XDI && transport != config.SPOOF {
			network := "tcp"
			if transport == config.UDP || transport == config.KCP || transport == config.QUIC {
				network = "udp"
			}
			if err := probeForwardListen(network, r.Config.Server.BindAddr); err != nil {
				return fmt.Errorf("forward tunnel listener %s/%s is unavailable: %w", network, r.Config.Server.BindAddr, err)
			}
		}
	}
	// Keep incomplete carriers impossible to start while their direction-aware
	// stream adapters are being added. This guard is widened only together with
	// an end-to-end test for that carrier.
	switch transport {
	case config.TCP, config.STEALTH, config.TCPMUX, config.KCP, config.XDI, config.SPOOF, config.QUIC, config.WS, config.WSS, config.WSMUX, config.WSSMUX, config.UDP:
		return nil
	default:
		return fmt.Errorf("forward direction for transport %q is not implemented in this build", transport)
	}
}

func probeForwardListen(network, addr string) error {
	if network == "udp" {
		pc, err := net.ListenPacket("udp", addr)
		if err != nil {
			return err
		}
		return pc.Close()
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return ln.Close()
}

func (p forwardProvider) Run(ctx context.Context, r Request) error {
	if err := p.Validate(ctx, r); err != nil {
		return err
	}
	if r.Config.HasClient() {
		waitMetrics := reverseMetrics(ctx, r, string(r.Config.Client.Transport), "iran-edge")
		c := client.NewForwardEdge(&r.Config.Client, ctx)
		go c.Start()
		<-ctx.Done()
		c.Stop()
		waitMetrics()
		return nil
	}
	waitMetrics := reverseMetrics(ctx, r, string(r.Config.Server.Transport), "kharej-origin")
	s := server.NewForwardOrigin(&r.Config.Server, ctx)
	go s.Start()
	<-ctx.Done()
	s.Stop()
	waitMetrics()
	return nil
}

func (forwardProvider) Health(context.Context, Request) (Health, error) {
	return Health{Ready: true, Detail: "forward transport process is running"}, nil
}
func (forwardProvider) Counters(_ context.Context, r Request) (Counters, error) {
	name := strings.TrimSuffix(filepath.Base(r.ConfigPath), filepath.Ext(r.ConfigPath))
	snap, err := metrics.Read(filepath.Dir(r.ConfigPath), name)
	if err != nil {
		return Counters{}, err
	}
	return Counters{RXBytes: snap.BytesIn, TXBytes: snap.BytesOut, RXPackets: snap.PacketsIn, TXPackets: snap.PacketsOut}, nil
}
func (forwardProvider) Cleanup(context.Context, Request) error { return nil }
