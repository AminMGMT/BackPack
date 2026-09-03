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

	// ExperimentalPanel makes "/" open the rebuilt panel at /panel/ instead of
	// the dashboard this file has always served.
	//
	// Off by default and on upgrade: the new panel is still being built, so a
	// server that has not asked for it must keep the finished one. See
	// panel.go, which also documents the way back for a browser that cannot
	// reach a control inside the new panel.
	ExperimentalPanel bool `json:"experimental_panel,omitempty"`

	// HTTPS, when set, serves the panel over TLS instead of plain HTTP.
	//
	// It is off by default and stays that way on upgrade: a panel reached at
	// http://ip:7777 keeps working exactly as it did. Turning it on is a
	// deliberate act, because it changes the address people have bookmarked.
	//
	// TLSDomain switches to Let's Encrypt for that name, which must resolve to
	// this server; empty means the generated self-signed certificate, which
	// works on a bare IP. Certificates renew themselves either way — an ACME
	// one is reissued well before its ninety days are up and picked up on the
	// next connection, with no restart.
	HTTPS     bool   `json:"https,omitempty"`
	TLSDomain string `json:"tls_domain,omitempty"`
	TLSEmail  string `json:"tls_email,omitempty"`
	// TLSSelfHost is an optional domain or IP to add to the self-signed
	// certificate's SANs, for reaching the panel by a name that has no public
	// DNS for Let's Encrypt (an internal domain, a host that is only in the
	// operator's /etc/hosts). It changes nothing about which addresses already
	// work — every local IP and loopback are always included — it only adds one
	// the machine cannot discover on its own. Empty is the common case.
	TLSSelfHost string `json:"tls_self_host,omitempty"`
}

// Scheme is the URL scheme the panel answers on.
func (c Config) Scheme() string {
	if c.HTTPS {
		return "https"
	}
	return "http"
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
	data, _ := json.MarshalIndent(c, "", "  ")
	// Atomic: the panel reads this on every login and the CLI shows the password
	// from it, so a truncated read would look like a wrong password.
	return app.WriteFileAtomic(app.WebUIConfig, data, 0600)
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

// SetPort persists a new panel port and restarts the panel service so it
// listens there. Used from the CLI (a separate process from the server).
func SetPort(port int) (Config, error) {
	c := Load()
	if port < 1 || port > 65535 {
		return c, fmt.Errorf("port must be between 1 and 65535")
	}
	c.Port = port
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
