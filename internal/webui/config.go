// Package webui serves an authenticated, dark-themed web dashboard on port
// 7777 showing live system metrics, tunnels and their logs.
package webui

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
)

// Config is the persisted web-panel configuration.
type Config struct {
	Password string `json:"password"` // 8-digit login password
	Port     int    `json:"port"`
}

// Load reads the saved config, filling defaults for missing fields.
func Load() Config {
	var c Config
	if data, err := os.ReadFile(app.WebUIConfig); err == nil {
		json.Unmarshal(data, &c)
	}
	if c.Port == 0 {
		c.Port = app.WebUIPort
	}
	return c
}

// Save persists the config (0600, root only).
func Save(c Config) error {
	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(app.WebUIConfig, data, 0600)
}

// EnsurePassword returns the config, generating and saving an 8-digit password
// if none exists yet.
func EnsurePassword() (Config, error) {
	c := Load()
	if c.Password == "" {
		c.Password = randomDigits(8)
		if err := Save(c); err != nil {
			return c, err
		}
	}
	return c, nil
}

// RegeneratePassword creates a new 8-digit password and restarts the panel.
func RegeneratePassword() (Config, error) {
	c := Load()
	c.Password = randomDigits(8)
	if err := Save(c); err != nil {
		return c, err
	}
	manage.RestartService(app.WebUIService)
	return c, nil
}

// SetPassword persists a custom password and restarts the panel service so the
// change takes effect. Used from the CLI (a separate process from the server).
func SetPassword(pw string) (Config, error) {
	c := Load()
	c.Password = pw
	if err := Save(c); err != nil {
		return c, err
	}
	manage.RestartService(app.WebUIService)
	return c, nil
}

// EnsureRunning makes sure a password exists and the web-panel systemd service
// is installed and running. Safe to call repeatedly (idempotent).
func EnsureRunning() (Config, error) {
	c, err := EnsurePassword()
	if err != nil {
		return c, err
	}
	unit := fmt.Sprintf(`[Unit]
Description=Backpack Web Panel
After=network.target

[Service]
Type=simple
ExecStart=%s --webui
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
`, app.BinPath)

	path := app.ServiceDir + "/" + app.WebUIService
	if err := os.WriteFile(path, []byte(unit), 0644); err != nil {
		return c, err
	}
	if err := manage.DaemonReload(); err != nil {
		return c, err
	}
	return c, manage.StartService(app.WebUIService)
}

// Disable stops and removes the web-panel service.
func Disable() error {
	if manage.IsActive(app.WebUIService) || manage.IsEnabled(app.WebUIService) {
		manage.DisableService(app.WebUIService)
	}
	os.Remove(app.ServiceDir + "/" + app.WebUIService)
	return manage.DaemonReload()
}

// Running reports whether the web-panel service is active.
func Running() bool {
	return manage.IsActive(app.WebUIService)
}

// randomDigits returns a cryptographically-random numeric string of length n.
func randomDigits(n int) string {
	b := make([]byte, n)
	for i := range b {
		d, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			b[i] = '0' + byte(i%10)
			continue
		}
		b[i] = '0' + byte(d.Int64())
	}
	return string(b)
}
