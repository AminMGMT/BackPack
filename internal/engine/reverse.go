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

func reverseMetrics(ctx context.Context, r Request, transport, role string) {
	c := metrics.NewCollector(filepath.Dir(r.ConfigPath), reverseName(r.ConfigPath), transport, role, nil, nil)
	done := make(chan struct{})
	go func() { <-ctx.Done(); close(done) }()
	_ = c.Write()
	go c.Run(done, 30*time.Second)
}

func (reverseProvider) Run(ctx context.Context, r Request) error {
	if err := (reverseProvider{}).Validate(ctx, r); err != nil {
		return err
	}
	if r.Config.HasServer() {
		reverseMetrics(ctx, r, string(r.Config.Server.Transport), "server")
		s := server.NewServer(&r.Config.Server, ctx)
		go s.Start()
		<-ctx.Done()
		s.Stop()
		return nil
	}
	reverseMetrics(ctx, r, string(r.Config.Client.Transport), "client")
	c := client.NewClient(&r.Config.Client, ctx)
	go c.Start()
	<-ctx.Done()
	c.Stop()
	return nil
}

// Reverse health remains socket-aware in manage; the registry adapter reports
// only that this provider has no local rule-set to inspect.
func (reverseProvider) Health(context.Context, Request) (Health, error) {
	return Health{Ready: true, Detail: "reverse health is connection-based"}, nil
}
func (reverseProvider) Counters(context.Context, Request) (Counters, error) { return Counters{}, nil }
func (reverseProvider) Cleanup(context.Context, Request) error              { return nil }
