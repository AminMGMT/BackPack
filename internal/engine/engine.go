// Package engine is the registry shared by the runtime and management planes.
// Engine selection is config-driven; mode is metadata and never persisted in
// the TOML.
package engine

import (
	"context"
	"fmt"
	"sync"

	"github.com/backpack/backpack/config"
)

type Metadata struct {
	Name string
	Mode string
}

type Request struct {
	ConfigPath string
	Config     *config.Config
	// Replacing means validation is evaluating a candidate for an instance
	// whose current process may still own its existing listen sockets.
	Replacing bool
}

type Health struct {
	Ready   bool
	Backend string
	Detail  string
	Drift   []string
}

type Counters struct {
	RXBytes, TXBytes     uint64
	RXPackets, TXPackets uint64
}

// Provider defines lifecycle semantics for every implementation. Validate and
// Health are read-only. Run owns a long-lived instance and returns startup or
// runtime failures. Cleanup is safe without a running process.
type Provider interface {
	Metadata() Metadata
	Validate(context.Context, Request) error
	Run(context.Context, Request) error
	Health(context.Context, Request) (Health, error)
	Counters(context.Context, Request) (Counters, error)
	Cleanup(context.Context, Request) error
}

var (
	mu        sync.RWMutex
	providers = map[config.EngineType]Provider{}
)

func Register(name config.EngineType, provider Provider) {
	mu.Lock()
	defer mu.Unlock()
	if name == "" || provider == nil {
		panic("engine: invalid registration")
	}
	if _, exists := providers[name]; exists {
		panic("engine: duplicate registration: " + string(name))
	}
	providers[name] = provider
}

func Get(name config.EngineType) (Provider, error) {
	if name == "" {
		name = config.EngineReverse
	}
	mu.RLock()
	p := providers[name]
	mu.RUnlock()
	if p == nil {
		return nil, fmt.Errorf("engine %q is not registered", name)
	}
	return p, nil
}

func Resolve(cfg *config.Config) (Provider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil configuration")
	}
	if err := cfg.ValidateStructure(); err != nil {
		return nil, err
	}
	return Get(cfg.EffectiveEngine())
}

func MetadataFor(cfg *config.Config) (Metadata, error) {
	p, err := Resolve(cfg)
	if err != nil {
		return Metadata{}, err
	}
	return p.Metadata(), nil
}

// CleanupOrphans removes only netfilter objects whose full structured
// ownership matches an identity in configDir. With all=false, live configs are
// left alone; uninstall passes all=true after stopping known services.
func CleanupOrphans(ctx context.Context, configDir string, all bool) error {
	return cleanupOrphans(ctx, configDir, all)
}
