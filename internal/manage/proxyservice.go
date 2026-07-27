package manage

import (
	"fmt"
	"os"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/localproxy"
)

// The built-in-proxy service.
//
// Optional and off until the operator turns it on. When enabled, this node
// serves its own SOCKS5 or HTTP proxy on a loopback port the operator chose,
// and a tunnel forwards to it — so there is nothing separate to install or keep
// running behind the tunnel. The unit is only present while the feature is on.

const proxyUnit = `[Unit]
Description=Backpack built-in proxy (SOCKS5/HTTP)
After=network.target

[Service]
Type=simple
ExecStart=%s --proxy
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`

// EnableProxyService saves the config, installs the unit and (re)starts it.
func EnableProxyService(cfg localproxy.Config) error {
	cfg.Enabled = true
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return fmt.Errorf("choose a port between 1 and 65535")
	}
	if err := localproxy.Save(cfg); err != nil {
		return err
	}
	path := app.ServiceDir + "/" + app.ProxyService
	if err := os.WriteFile(path, fmt.Appendf(nil, proxyUnit, app.BinPath), 0644); err != nil {
		return err
	}
	if err := DaemonReload(); err != nil {
		return err
	}
	// StartService enables and starts; if it is already running, a config change
	// must take effect, so restart instead (start is a no-op on an active unit).
	if IsActive(app.ProxyService) {
		return RestartService(app.ProxyService)
	}
	return StartService(app.ProxyService)
}

// DisableProxyService stops and removes the unit and records the choice.
func DisableProxyService() error {
	if IsActive(app.ProxyService) || IsEnabled(app.ProxyService) {
		DisableService(app.ProxyService)
	}
	os.Remove(app.ServiceDir + "/" + app.ProxyService)
	cfg := localproxy.Load()
	cfg.Enabled = false
	_ = localproxy.Save(cfg)
	return DaemonReload()
}

// ProxyRunning reports whether the built-in proxy service is active.
func ProxyRunning() bool { return IsActive(app.ProxyService) }
