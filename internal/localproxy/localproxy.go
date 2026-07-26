// Package localproxy runs a built-in proxy on the tunnel's exit side, so a
// node can be its own backend: instead of forwarding to a separate service
// (xray, a panel, …) that has to be installed and kept running, the tunnel
// forwards to a proxy this binary serves itself.
//
// It is optional and off by default. The operator picks the port — nothing is
// assumed — and maps a forwarded port to 127.0.0.1:<that port> like any other
// backend, so the tunnel engine is untouched.
package localproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/socks"
	"github.com/backpack/backpack/internal/utils"
)

// Kind is which proxy protocol to serve on the port.
type Kind string

const (
	SOCKS5 Kind = "socks5"
	HTTP   Kind = "http"
)

// Config is the persisted built-in-proxy configuration.
type Config struct {
	Enabled bool `json:"enabled"`
	// Type is "socks5" or "http".
	Type Kind `json:"type"`
	// Port is chosen by the operator; there is no default, so nothing starts
	// listening on a well-known port by surprise.
	Port int `json:"port"`
	// Username/Password gate the proxy. Empty means no authentication, which is
	// safe here: the proxy binds loopback and is only reachable through the
	// token-authenticated tunnel.
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// Dir is where the config lives; a variable so tests can point it elsewhere.
var Dir = app.ConfigDir

func path() string { return filepath.Join(Dir, "proxy.json") }

// Load reads the config, returning a disabled zero value if none exists.
func Load() Config {
	var c Config
	if data, err := os.ReadFile(path()); err == nil {
		json.Unmarshal(data, &c)
	}
	if c.Type == "" {
		c.Type = SOCKS5
	}
	return c
}

// Save persists the config (0600 — it may hold a password).
func Save(c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return app.WriteFileAtomic(path(), data, 0600)
}

// authFunc turns the configured credentials into a checker, or nil for no-auth.
func (c Config) authFunc() socks.AuthFunc {
	if c.Username == "" && c.Password == "" {
		return nil
	}
	return func(u, p string) bool { return u == c.Username && p == c.Password }
}

// Addr is the loopback address the proxy listens on. Loopback only: the proxy
// is meant to be reached through the tunnel, never from the open internet.
func (c Config) Addr() string { return fmt.Sprintf("127.0.0.1:%d", c.Port) }

// Run serves the configured proxy until ctx is cancelled. It is a no-op when
// the proxy is disabled or has no port, so the service can run unconditionally.
func Run(ctx context.Context) {
	c := Load()
	if !c.Enabled || c.Port <= 0 {
		<-ctx.Done()
		return
	}
	logger := utils.NewLogger("info")
	logger.Infof("built-in %s proxy listening on %s", c.Type, c.Addr())

	switch c.Type {
	case HTTP:
		if err := serveHTTP(ctx, c.Addr(), c.authFunc()); err != nil {
			logger.Errorf("http proxy stopped: %v", err)
		}
	default: // socks5
		if err := socks.Serve(ctx, c.Addr(), c.authFunc()); err != nil {
			logger.Errorf("socks5 proxy stopped: %v", err)
		}
	}
}
