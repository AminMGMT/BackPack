package node

import (
	"fmt"
	"os"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
)

// The node agent's service.
//
// It exists only on a managed server, and it is the reason the panel never
// needs a login: the privilege to write a config and talk to systemd is held
// here, locally, by a unit the operator installed themselves. What arrives over
// the channel is a request to use it in one of the ways ops.go allows.

const agentUnit = `[Unit]
Description=Backpack node agent (managed by a Backpack panel)
After=network.target

[Service]
Type=simple
ExecStart=%s node run
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`

// Install writes the agent config and its unit, then starts it.
func Install(cfg AgentConfig) error {
	if cfg.Server == "" {
		return fmt.Errorf("the panel address is required")
	}
	if cfg.HubKey == "" {
		return fmt.Errorf("the setup key is required")
	}
	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		return err
	}
	if err := SaveAgent(cfg); err != nil {
		return err
	}
	path := app.ServiceDir + "/" + app.NodeService
	if err := os.WriteFile(path, fmt.Appendf(nil, agentUnit, app.BinPath), 0644); err != nil {
		return err
	}
	if err := manage.DaemonReload(); err != nil {
		return err
	}
	// Restart rather than start when it is already running, so re-running the
	// setup command against a different panel takes effect instead of silently
	// leaving the old connection up.
	if manage.IsActive(app.NodeService) {
		return manage.RestartService(app.NodeService)
	}
	return manage.StartService(app.NodeService)
}

// Uninstall stops the agent and removes its unit and credential.
//
// The tunnels stay. They are systemd services on this machine with configs on
// this disk, and they have nothing to do with the channel that was used to
// write them — which is the point: unmanaging a server must not be a way to
// take its traffic down.
func Uninstall() error {
	if manage.IsActive(app.NodeService) || manage.IsEnabled(app.NodeService) {
		manage.DisableService(app.NodeService)
	}
	os.Remove(app.ServiceDir + "/" + app.NodeService)
	os.Remove(AgentPath)
	return manage.DaemonReload()
}

// Running reports whether the agent service is up.
func Running() bool { return manage.IsActive(app.NodeService) }

// IsManaged reports whether this server has been enrolled with a panel.
func IsManaged() bool {
	c, err := LoadAgent()
	return err == nil && c.NodeKey != ""
}
