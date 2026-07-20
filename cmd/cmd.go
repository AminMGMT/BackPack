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

func Run(configPath string, ctx context.Context) {
	// Load and parse the configuration file
	cfg, err := loadConfig(configPath)
	if err != nil {
		logger.Fatalf("failed to load configuration: %v", err)
	}

	// Apply default values to the configuration
	applyDefaults(cfg)

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
		if !cfg.Server.SkipOptz {
			ApplyTCPTuning()
		}

		startMetrics(ctx, configPath, string(cfg.Server.Transport), "server")

		srv := server.NewServer(&cfg.Server, ctx) // server
		go srv.Start()

		// Wait for shutdown signal
		<-ctx.Done()
		srv.Stop()
		logger.Println("shutting down server...")
	case "client":
		// Apply temporary TCP optimizations at startup
		if !cfg.Client.SkipOptz {
			ApplyTCPTuning()
		}

		startMetrics(ctx, configPath, string(cfg.Client.Transport), "client")

		clnt := client.NewClient(&cfg.Client, ctx) // client
		go clnt.Start()

		// Wait for shutdown signal
		<-ctx.Done()
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
