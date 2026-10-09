package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/metrics"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/client"

	"github.com/backpack/backpack/internal/server"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/utils/network"

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
//
// The function it returns stops the collector and waits for its last write.
// Every engine calls it on the way out: a reload starts the next generation's
// collector straight after, and that one takes its baseline from the file —
// read before the last write is in, the traffic since the previous tick was
// lost from the total.
func startMetrics(ctx context.Context, configPath, transport, role string) (stop func()) {
	return startMetricsWithTraffic(ctx, configPath, transport, role, nil, nil)
}

// startMetricsWithTraffic is the same, for an engine that counts its own
// traffic rather than reporting it through metrics.AddBytes.
//
// The reverse transports call AddBytes from inside their copy loops, which is
// where the bytes pass. The layer-3 engine has no copy loop — it moves whole
// packets between a device and a carrier — and already keeps exact counters of
// its own, so it hands them over to be read once per snapshot instead. Without
// this its panel card showed 0 in and 0 out for a tunnel that was carrying
// traffic, which reads as a tunnel that is up but doing nothing.
func startMetricsWithTraffic(
	ctx context.Context,
	configPath, transport, role string,
	bytesIn, bytesOut func() uint64,
) (stop func()) {
	name := tunnelNameFromPath(configPath)

	// The same three facts identify a line in a shipped log as identify a
	// snapshot on disk, and both are known exactly here and nowhere earlier —
	// so they are set together rather than derived twice. It has to happen
	// before the engine builds its logger, which is why this call comes before
	// NewServer and NewClient in every path. See utils/logident.go.
	utils.SetLogIdentity(utils.LogIdentity{Tunnel: name, Role: role, Transport: transport})

	if name == "" {
		return func() {}
	}
	c := metrics.NewCollector(filepath.Dir(configPath), name, transport, role, bytesIn, bytesOut)
	liveCollector.Store(c)
	ctx, cancel := context.WithCancel(ctx)
	written := make(chan struct{})
	go func() {
		defer close(written)
		_ = c.Write() // an immediate first reading, so the file exists right away
		c.Run(ctx.Done(), 30*time.Second)
	}()
	return func() {
		liveCollector.CompareAndSwap(c, nil)
		cancel()
		<-written
	}
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
	if err := validateConfig(cfg); err != nil {
		logger.Fatalf("%v", err)
	}

	// The kernel tuning is process-wide and does not depend on anything in the
	// file that a reload can change, so it is applied once rather than on every
	// reload.
	tuned := false

	// The local control socket, so that whatever is watching this tunnel has a
	// rung below `systemctl restart`. It lives for the whole process rather
	// than for one generation: a caller asking what the engine is doing while
	// it is between transports should get an answer, not a closed socket.
	// See internal/enginectl.
	ctl := &engineControl{name: tunnelNameFromPath(configPath)}
	serveEngineControl(ctx, ctl)

	for {
		// A tunnel that has used its traffic limit up does not start until the
		// limit is raised. The file may have changed while it waited, so it is
		// read again rather than starting what was loaded before. See quota.go.
		ok, waited := awaitQuota(ctx, configPath)
		if !ok {
			return
		}
		if waited {
			if next, err := loadConfig(configPath); err == nil {
				applyDefaults(next)
				if validateConfig(next) == nil {
					cfg = next
				}
			}
		}

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
		// Ending this generation is exactly what a configuration change does,
		// so a restart asked for over the socket takes the path a reload takes
		// rather than a second way of stopping a transport.
		ctl.setGeneration(cancel, cfg)
		go func() {
			defer close(done)
			runEngine(&running, runCtx, configPath, applyTuning)
		}()
		go watchQuota(runCtx, configPath, cancel)

		next, why := awaitConfigChange(ctx, runCtx, configPath, cfg)
		cancel()
		<-done

		switch why {
		case wakeShutdown:
			return

		case wakeRestart:
			if quotaReached(configPath) {
				// Ended by its traffic limit; awaitQuota holds it at the top.
				continue
			}
			// A restart asked for over the control socket. It is not a reload
			// and is not reported as one: the same configuration starts again,
			// after its ports come free.
			logger.Info("the transport was asked to restart; starting it again")
			waitForPorts(ctx, portsInUse(cfg))
			continue
		}

		logger.Info("the configuration file changed; restarting the tunnel with it")
		waitForPorts(ctx, portsInUse(cfg))
		cfg = next
	}
}

// runEngine runs one tunnel until ctx ends.
func runEngine(cfg *config.Config, ctx context.Context, configPath string, applyTuning bool) {
	cleanupPck := func() {
		network.CleanupPckGuards(cfg.Server.Token, cfg.Client.Token, cfg.Direct.Token, cfg.L3.Token)
	}
	// Recover orphaned tags even if an earlier tunnel was deleted or its token
	// was replaced. The ownership checks preserve every live PCK carrier.
	network.CleanupPckGuards()
	defer cleanupPck()

	// A layer-3 tunnel is a different thing from a port forwarder and shares
	// none of the machinery below. It is dispatched here, before any of it, so
	// that the reverse path is reached by exactly the configurations that
	// always reached it: an [l3] table is the only way past this branch, and
	// no configuration written before it existed has one.
	if cfg.L3.Enabled() {
		runL3Tunnel(cfg, ctx, configPath)
		return
	}
	if cfg.Direct.Enabled() {
		runDirectTunnel(cfg, ctx, configPath)
		return
	}

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

		stopMetrics := startMetrics(ctx, configPath, string(cfg.Server.Transport), "server")
		defer stopMetrics()

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

		stopMetrics := startMetrics(ctx, configPath, string(cfg.Client.Transport), "client")
		defer stopMetrics()

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

// CheckConfigFile is the load-time validation Run applies to a config file,
// without starting anything: `backpack check` asks it, so the question "would
// this start?" has one answer, the engine's. See cli.EngineCheck.
//
// The engine logs to stdout, which is right under systemd and wrong here:
// `backpack check --json` owns stdout, and a warning ahead of the JSON breaks
// whatever parses it. For the check, the log goes to stderr.
func CheckConfigFile(path string) error {
	out := logger.Out
	logger.SetOutput(os.Stderr)
	defer logger.SetOutput(out)

	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}
	applyDefaults(cfg)
	return validateConfig(cfg)
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
