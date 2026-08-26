package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tunnel/direct"
	"github.com/backpack/backpack/internal/utils"
	"github.com/sirupsen/logrus"
)

// The direct tunnel's entry point.
//
// A separate file from cmd.go for the same reason internal/tunnel/direct is a
// separate package: the direct path should be readable, changeable and
// removable without passing through the reverse tunnel's startup, and the one
// branch in runEngine is the whole of the contact between them.
//
// Unlike the reverse path this applies no TCP tuning. The tuning targets the
// kernel's global settings on behalf of a large connection pool, and a direct
// tunnel has no pool: one mux session carries every connection.

// directRestartDelay is how long to wait before rebuilding a tunnel whose Run
// returned an error. Reaching here means a listener could not be bound or a
// port was already taken — the session's own reconnects are handled inside the
// engine and never surface as a restart.
const directRestartDelay = 5 * time.Second

// runDirectTunnel keeps one direct tunnel running until ctx ends.
func runDirectTunnel(cfg *config.Config, ctx context.Context, configPath string) {
	logger := utils.NewLoggerWithFormat(directLogLevel(cfg), "")

	dc := cfg.Direct
	tunnelCfg := direct.Config{
		Role:             dc.ResolvedRole(),
		Addr:             dc.Addr,
		Token:            dc.Token,
		Transport:        dc.Transport,
		ServerName:       dc.ServerName,
		TLSCertFile:      dc.TLSCertFile,
		TLSKeyFile:       dc.TLSKeyFile,
		ACMEDomain:       dc.ACMEDomain,
		ACMEEmail:        dc.ACMEEmail,
		Ports:            dc.Ports,
		AcceptUDP:        dc.AcceptUDP,
		MaxConnections:   dc.MaxConnections,
		BandwidthMbps:    dc.BandwidthMbps,
		Sessions:         dc.Sessions,
		DialTimeout:      time.Duration(dc.DialTimeout) * time.Second,
		RetryDelay:       time.Duration(dc.RetryInterval) * time.Second,
		Keepalive:        time.Duration(dc.Keepalive) * time.Second,
		Nodelay:          dc.Nodelay,
		MSS:              dc.MSS,
		MuxVersion:       dc.MuxVersion,
		MaxFrameSize:     dc.MaxFrameSize,
		MaxReceiveBuffer: dc.MaxReceiveBuffer,
		MaxStreamBuffer:  dc.MaxStreamBuffer,
	}

	// Built before anything is opened, so a bad configuration is a startup
	// error the operator can see rather than a tunnel that retries silently.
	runner, role, err := newDirectRunner(tunnelCfg, logger)
	if err != nil {
		logger.Fatalf("direct tunnel configuration is not usable: %v", err)
		return
	}

	startMetrics(ctx, configPath, "direct-"+tunnelCfg.Transport, role)

	for {
		if err := runner(ctx); err != nil && ctx.Err() == nil {
			logger.Errorf("direct tunnel stopped: %v — restarting in %s", err, directRestartDelay)
		}
		if ctx.Err() != nil {
			logger.Info("shutting down the direct tunnel...")
			return
		}
		select {
		case <-ctx.Done():
			logger.Info("shutting down the direct tunnel...")
			return
		case <-time.After(directRestartDelay):
		}
	}
}

// newDirectRunner builds whichever half this configuration describes, and
// returns the role name the metrics file should record.
// The role is checked here rather than left to the engine. NewEdge and
// NewOrigin each set the role themselves before validating, so the engine's own
// "unknown role" branch can never be reached through them — and a typo like
// role = "kharje" on the kharej server would fall through to the edge and be
// built as the Iran side. That fails, but it fails complaining about a missing
// port list, which says nothing about the letter that was wrong.
func newDirectRunner(cfg direct.Config, logger *logrus.Logger) (func(context.Context) error, string, error) {
	switch cfg.Role {
	case direct.RoleOrigin:
		origin, err := direct.NewOrigin(cfg, logger)
		if err != nil {
			return nil, "", err
		}
		return origin.Run, "kharej-origin", nil
	case direct.RoleEdge:
		edge, err := direct.NewEdge(cfg, logger)
		if err != nil {
			return nil, "", err
		}
		return edge.Run, "iran-edge", nil
	default:
		return nil, "", fmt.Errorf(
			"role %q is not one this tunnel has: write \"iran\" on the Iran server "+
				"(it exposes the ports and dials out) or \"kharej\" on the server abroad",
			cfg.Role)
	}
}

// directLogLevel borrows whichever level the file already sets, so a [direct]
// tunnel need not repeat a key the operator has set once.
func directLogLevel(cfg *config.Config) string {
	if cfg.Server.LogLevel != "" {
		return cfg.Server.LogLevel
	}
	return cfg.Client.LogLevel
}
