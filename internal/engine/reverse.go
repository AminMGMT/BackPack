package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/client"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/server"
)

type reverseProvider struct{}

func init()                                { Register(config.EngineReverse, reverseProvider{}) }
func (reverseProvider) Metadata() Metadata { return Metadata{Name: "reverse", Mode: "reverse"} }
func (reverseProvider) Validate(_ context.Context, r Request) error {
	if r.Config == nil {
		return fmt.Errorf("nil reverse configuration")
	}
	return r.Config.ValidateStructure()
}

func reverseName(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// reverseMetrics starts the process-wide collector and returns a waiter for its
// final atomic snapshot. Providers must call the waiter after ctx is cancelled;
// otherwise systemd can let the process exit while the final rename is still
// pending, losing the last interval of cumulative counters.
func reverseMetrics(ctx context.Context, r Request, transport, role string) func() {
	c := metrics.NewCollector(filepath.Dir(r.ConfigPath), reverseName(r.ConfigPath), transport, role, nil, nil)
	_ = c.Write()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		c.Run(ctx.Done(), 30*time.Second)
	}()
	return func() { <-finished }
}

func (reverseProvider) Run(ctx context.Context, r Request) error {
	if err := (reverseProvider{}).Validate(ctx, r); err != nil {
		return err
	}
	if r.Config.HasServer() {
		waitMetrics := reverseMetrics(ctx, r, string(r.Config.Server.Transport), "server")
		s := server.NewServer(&r.Config.Server, ctx)
		go s.Start()
		<-ctx.Done()
		s.Stop()
		waitMetrics()
		return nil
	}
	waitMetrics := reverseMetrics(ctx, r, string(r.Config.Client.Transport), "client")
	c := client.NewClient(&r.Config.Client, ctx)
	go c.Start()
	<-ctx.Done()
	c.Stop()
	waitMetrics()
	return nil
}

// Reverse health remains socket-aware in manage; the registry adapter reports
// only that this provider has no local rule-set to inspect.
func (reverseProvider) Health(context.Context, Request) (Health, error) {
	return Health{Ready: true, Detail: "reverse health is connection-based"}, nil
}
func (reverseProvider) Counters(_ context.Context, r Request) (Counters, error) {
	snap, err := metrics.Read(filepath.Dir(r.ConfigPath), reverseName(r.ConfigPath))
	if err != nil {
		return Counters{}, err
	}
	return Counters{RXBytes: snap.BytesIn, TXBytes: snap.BytesOut, RXPackets: snap.PacketsIn, TXPackets: snap.PacketsOut}, nil
}
func (reverseProvider) Cleanup(context.Context, Request) error { return nil }
