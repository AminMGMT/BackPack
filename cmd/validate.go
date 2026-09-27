package cmd

import (
	"errors"
	"fmt"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tunnel/l3"
)

// checkEngine reports whether the engine this configuration selects can be
// built from it — the checks the engines themselves make before opening
// anything, run early so a reload can refuse a file instead of dying on it.
func checkEngine(cfg *config.Config) error {
	switch {
	case cfg.L3.Enabled():
		tunnelCfg, err := l3ConfigOf(cfg)
		if err != nil {
			return fmt.Errorf("layer-3 tunnel: %w", err)
		}
		if _, err := l3.New(tunnelCfg, nil); err != nil {
			return fmt.Errorf("layer-3 tunnel configuration is not usable: %w", err)
		}
		if _, err := l3.NewForwarder(tunnelCfg, nil); err != nil {
			return fmt.Errorf("layer-3 tunnel port mappings are not usable: %w", err)
		}
		return nil

	case cfg.Direct.Enabled():
		if _, _, err := newDirectRunner(directConfigOf(cfg), nil); err != nil {
			return fmt.Errorf("direct tunnel configuration is not usable: %w", err)
		}
		return nil
	}

	switch {
	case cfg.Server.BindAddr != "":
		if !config.IsReverseTransport(cfg.Server.Transport) {
			return fmt.Errorf("[server] transport %q is not one this engine has", cfg.Server.Transport)
		}
	case cfg.Client.RemoteAddr != "":
		if !config.IsReverseTransport(cfg.Client.Transport) {
			return fmt.Errorf("[client] transport %q is not one this engine has", cfg.Client.Transport)
		}
	default:
		return errors.New("neither server nor client configuration is properly set")
	}
	return nil
}
