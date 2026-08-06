package cmd

import (
	"context"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/engine"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
)

var (
	logger = utils.NewLogger("info")
)

// Run keeps one tunnel running from a configuration file, restarting it in
// place whenever the file changes. See reload.go for why the file is watched at
// all, and for the two rules that keep watching it from being a liability: a
// file that does not parse is ignored, and a file that means the same thing
// does not disturb the tunnel.
func Run(configPath string, ctx context.Context) {
	// The first load is the one that must succeed: there is no running tunnel
	// to fall back to, so a bad file here is fatal exactly as it always was.
	cfg, err := loadConfig(configPath)
	if err != nil {
		logger.Fatalf("failed to load configuration: %v", err)
	}
	applyDefaults(cfg)

	// The kernel tuning is process-wide and does not depend on anything in the
	// file that a reload can change, so it is applied once rather than on every
	// reload.
	tuned := false

	for {
		// The engine mutates the configuration it is given — the transports
		// write their status back into it — so it gets its own copy and the
		// pristine one is kept for comparing against the file.
		running := *cfg

		// Decided here rather than inside the goroutine, which would be reading
		// the flag while this loop writes it.
		applyTuning := !tuned
		tuned = true

		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			if err := runEngine(&running, runCtx, configPath, applyTuning); err != nil {
				logger.Fatalf("engine failed: %v", err)
			}
		}()

		next := awaitConfigChange(ctx, configPath, cfg)
		cancel()
		<-done

		if next == nil {
			return // ctx ended: shutting down, not reloading
		}

		logger.Info("the configuration file changed; restarting the tunnel with it")
		waitForPorts(ctx, portsInUse(cfg))
		cfg = next
	}
}

// runEngine runs one tunnel until ctx ends.
func runEngine(cfg *config.Config, ctx context.Context, configPath string, applyTuning bool) error {
	provider, err := engine.Resolve(cfg)
	if err != nil {
		return err
	}
	if cfg.EffectiveEngine() == config.EngineReverse || cfg.EffectiveEngine() == config.EngineForward {
		if cfg.HasServer() {
			// Apply temporary TCP optimizations at startup
			if applyTuning && !cfg.Server.SkipOptz {
				ApplyTCPTuning()
			}
		} else {
			// Apply temporary TCP optimizations at startup
			if applyTuning && !cfg.Client.SkipOptz {
				ApplyTCPTuning()
			}
		}
		go func() {
			select {
			case <-ctx.Done():
			case <-time.After(100 * time.Millisecond):
				reportZeroCopy(ctx)
			}
		}()
	}
	return provider.Run(ctx, engine.Request{ConfigPath: configPath, Config: cfg})
}

// loadConfig loads and parses the TOML configuration file.
func loadConfig(configPath string) (*config.Config, error) {
	return config.LoadFile(configPath)
}

// reportZeroCopy says, periodically and in the tunnel's own journal, whether
// the kernel forwarding path is being used.
//
// Enabling it and having it work are different things: it declines silently on
// a mux or websocket transport, on a rate-limited tunnel, and off Linux. An
// operator who switched it on to try it has no way to tell which happened, and
// the counters live in this process — not in the CLI that runs Health Check —
// so this is where they have to be said out loud.
//
// It is deliberately in the log rather than the panel: the point of it is to be
// pasted into a bug report alongside everything else from the same minutes.
func reportZeroCopy(ctx context.Context) {
	if !handlers.ZeroCopy() {
		return
	}
	logger.Infof("zero-copy forwarding is enabled (experimental; plain tcp transport on Linux only) — %s", handlers.RelaySummary())

	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				logger.Infof("zero-copy forwarding: %s", handlers.RelaySummary())
			}
		}
	}()
}
