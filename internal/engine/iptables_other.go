//go:build !linux

package engine

import (
	"context"
	"fmt"

	"github.com/backpack/backpack/config"
)

type iptablesProvider struct{}

func init()                                 { Register(config.EngineIPTables, iptablesProvider{}) }
func (iptablesProvider) Metadata() Metadata { return Metadata{Name: "iptables", Mode: "direct"} }
func unsupported() error {
	return fmt.Errorf("iptables direct forwarding is unsupported on this operating system; Linux is required")
}
func (iptablesProvider) Validate(context.Context, Request) error { return unsupported() }
func (iptablesProvider) Run(context.Context, Request) error      { return unsupported() }
func (iptablesProvider) Health(context.Context, Request) (Health, error) {
	return Health{Detail: unsupported().Error()}, nil
}
func (iptablesProvider) Counters(context.Context, Request) (Counters, error) {
	return Counters{}, unsupported()
}
func (iptablesProvider) Cleanup(context.Context, Request) error { return unsupported() }
func cleanupOrphans(context.Context, string, bool) error        { return nil }
func RemoveRuntimeArtifacts() error                             { return nil }
