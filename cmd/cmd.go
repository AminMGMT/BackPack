package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/metrics"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/client"

	"github.com/backpack/backpack/internal/server"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"

	"github.com/BurntSushi/toml"
)

var (
	logger = utils.NewLogger("info")
)

// tunnelNameFromPath derives a tunnel's name from its config path, which is
// how the rest of the tool identifies it.
func tunnelNameFromPath(configPath string) string {
	base := filepath.Base(configPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// startMetrics records what the tunnel carries so the CLI can show it later.
// It is best-effort: a tunnel must never fail because diagnostics could not be
// written.
func startMetrics(ctx context.Context, configPath, transport, role string) {
	name := tunnelNameFromPath(configPath)
	if name == "" {
		return
	}
	c := metrics.NewCollector(filepath.Dir(configPath), name, transport, role, nil, nil)
	go func() {
		done := make(chan struct{})
		go func() { <-ctx.Done(); close(done) }()
		_ = c.Write() // an immediate first reading, so the file exists right away
		c.Run(done, 30*time.Second)
	}()
}

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
			runEngine(&running, runCtx, configPath, applyTuning)
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
func runEngine(cfg *config.Config, ctx context.Context, configPath string, applyTuning bool) {
	configType := ""
	if cfg.Server.BindAddr != "" {
		configType = "server"
	} else if cfg.Client.RemoteAddr != "" {
		configType = "client"
	} else {
		logger.Fatalf("neither server nor client configuration is properly set.")
	}

	// Determine whether to run as a server or client
	switch configType {
	case "server":
		// Apply temporary TCP optimizations at startup
		if applyTuning && !cfg.Server.SkipOptz {
			ApplyTCPTuning()
		}

		startMetrics(ctx, configPath, string(cfg.Server.Transport), "server")

		srv := server.NewServer(&cfg.Server, ctx) // server
		reportZeroCopy(ctx)
		// Start blocks until its own context ends and everything it owns has
		// stopped. Running it in a goroutine and waiting on ctx here instead
		// meant this returned the moment the signal arrived, while the parts of
		// the generation that shut down in their own time were still holding
		// their ports — and a reload starts the next generation as soon as this
		// function returns.
		srv.Start()
		srv.Stop()
		logger.Println("shutting down server...")
	case "client":
		// Apply temporary TCP optimizations at startup
		if applyTuning && !cfg.Client.SkipOptz {
			ApplyTCPTuning()
		}

		startMetrics(ctx, configPath, string(cfg.Client.Transport), "client")

		clnt := client.NewClient(&cfg.Client, ctx) // client
		reportZeroCopy(ctx)
		// Blocks until the generation is over; see the server case above.
		clnt.Start()
		clnt.Stop()
		logger.Println("shutting down client...")

	default:
		logger.Fatalf("neither server nor client configuration is properly set.")

	}
}

// loadConfig loads and parses the TOML configuration file.
func loadConfig(configPath string) (*config.Config, error) {
	var cfg config.Config
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		return &cfg, err
	}
	return &cfg, nil
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
