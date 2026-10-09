package cmd

import (
	"context"
	"sync"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tunnel/l3"
	"github.com/backpack/backpack/internal/utils"
)

// The layer-3 tunnel's entry point.
//
// It is a separate file from cmd.go for the same reason the engine is a
// separate package: everything about the direct layer-3 path should be
// possible to read, change or remove without passing through the reverse
// tunnel's startup, and the one branch in runEngine is the whole of the
// contact between them.
//
// Two differences from the reverse path are worth stating. There is no TCP
// tuning here, because the tuning targets forwarded TCP connections and a
// layer-3 tunnel forwards none. And there is no fatal exit on a failed start:
// the engine retries, because the peer being unreachable at boot is ordinary
// on a machine whose network comes up after its services do.

// l3RestartDelay is how long to wait before rebuilding a tunnel that stopped
// on an error. Failures here are things like a TUN device that could not be
// created or a carrier port already taken, which a retry resolves only once
// the operator or the system has changed something.
const l3RestartDelay = 5 * time.Second

// runL3Tunnel keeps one layer-3 tunnel running until ctx ends.
func runL3Tunnel(cfg *config.Config, ctx context.Context, configPath string) {
	logger := utils.NewLoggerWithFormat(l3LogLevel(cfg), l3LogFormat(cfg))

	tunnelCfg, err := l3ConfigOf(cfg)
	if err != nil {
		logger.Fatalf("layer-3 tunnel: %v", err)
		return
	}

	// Validated once, before anything is opened. A configuration that cannot
	// work is worth failing on rather than retrying every five seconds
	// forever, and the operator needs to see why.
	tunnel, err := l3.New(tunnelCfg, logger)
	if err != nil {
		logger.Fatalf("layer-3 tunnel configuration is not usable: %v", err)
		return
	}

	// Built before anything is opened, so a bad port mapping is a startup
	// error rather than a tunnel that comes up serving half its ports. Nil
	// when the config forwards no ports, which is the plain layer-3 case.
	forwarder, err := l3.NewForwarder(tunnelCfg, logger)
	if err != nil {
		logger.Fatalf("layer-3 tunnel port mappings are not usable: %v", err)
		return
	}
	if forwarder != nil {
		forwarder.SetTunnelState(tunnel.Up)
	}

	// The engine's own counters, read once per snapshot. They survive the
	// restart loop below because the tunnel object does.
	stopMetrics := startMetricsWithTraffic(ctx, configPath, "l3-"+tunnelCfg.Carrier, l3Role(tunnelCfg.Mode),
		func() uint64 { return tunnel.Stats().BytesIn },
		func() uint64 { return tunnel.Stats().BytesOut },
	)
	defer stopMetrics()

	// The forwarder runs beside the tunnel rather than inside it, and outlives
	// any number of tunnel restarts. A user's connection to a forwarded port
	// has no reason to be dropped because the tunnel resealed its session, and
	// rebinding the listeners on every restart would race the ports against
	// the generation replacing them.
	var forwarding sync.WaitGroup
	if forwarder != nil {
		forwarding.Add(1)
		go func() {
			defer forwarding.Done()
			for {
				if err := forwarder.Run(ctx); err != nil && ctx.Err() == nil {
					logger.Errorf("layer-3 port forwarding stopped: %v - restarting in %s", err, l3RestartDelay)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(l3RestartDelay):
				}
			}
		}()
	}
	defer forwarding.Wait()

	for {
		if err := tunnel.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Errorf("layer-3 tunnel stopped: %v — restarting in %s", err, l3RestartDelay)
		}
		if ctx.Err() != nil {
			logger.Info("shutting down the layer-3 tunnel...")
			return
		}
		select {
		case <-ctx.Done():
			logger.Info("shutting down the layer-3 tunnel...")
			return
		case <-time.After(l3RestartDelay):
		}
	}
}

// l3LogLevel borrows whichever level the file already sets, so an [l3] tunnel
// need not repeat a key the operator has set once. Empty is the logger's own
// default.
func l3LogLevel(cfg *config.Config) string {
	if cfg.Server.LogLevel != "" {
		return cfg.Server.LogLevel
	}
	return cfg.Client.LogLevel
}

// l3LogFormat reads log_format from whichever of the two tables the file has,
// the same way l3LogLevel reads the level.
//
// It was hardcoded to the text format, so `log_format = "json"` on a layer-3
// tunnel was accepted, written into the file, shown in the menus, and ignored.
func l3LogFormat(cfg *config.Config) string {
	if cfg.Server.LogFormat != "" {
		return cfg.Server.LogFormat
	}
	return cfg.Client.LogFormat
}

// l3Role is what the metrics file records. The geography is what an operator
// recognises, and it is stable across the direction the tunnel happens to be
// dialled in.
func l3Role(mode string) string {
	if mode == l3.ModeDial {
		return "iran-edge"
	}
	return "kharej-origin"
}

// l3ConfigOf turns an [l3] table into the engine's configuration. Shared by the
// engine and by validateConfig, so a reload is judged by exactly what would run.
func l3ConfigOf(cfg *config.Config) (l3.Config, error) { return l3.ConfigFromTable(cfg.L3) }
