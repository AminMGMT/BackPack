// Package webui serves an authenticated, dark-themed web dashboard on port
// 7777 showing live system metrics, tunnels and their logs.
package webui

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
)

// Config is the persisted web-panel configuration.
type Config struct {
	Password string `json:"password"` // 8-digit login password
	Port     int    `json:"port"`

	// BasePath is the secret path segment the whole panel lives under, so it
	// answers at http://host:7777/<BasePath>/ and at nothing else.
	//
	// It is not authentication and does not pretend to be — the password is
	// still what lets anybody in. What it changes is who ever reaches the
	// password prompt. A panel on a known port at "/" is found by anything that
	// sweeps the internet within hours of being started, and from then on it is
	// answering login attempts from strangers forever. Behind a path nobody can
	// guess, those sweeps get a 404 and go away, and the login page is seen by
	// people who were told where it is.
	//
	// Generated on first use and on upgrade, so every panel has one; see
	// EnsureBasePath. "/" turns it off for an operator who wants the panel at
	// the root, which is the only way to get that now.
	BasePath string `json:"base_path,omitempty"`

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
// and a base path if either is missing.
//
// The base path is generated here rather than only on a fresh install, so a
// panel that has been running at "/" for a year gets one on the next upgrade.
// That does move the address: the CLI's Web Panel screen prints the whole URL,
// including the path, which is where an operator whose bookmark stopped working
// is told to look — and the address is on the machine they already have a shell
// on, which is the one place it can be found without being findable by anybody
// else.
func EnsurePassword() (Config, error) {
	c := Load()
	changed := false
	if c.Password == "" {
		c.Password = randomDigits(8)
		changed = true
	}
	if c.BasePath == "" {
		c.BasePath = randomPathSegment()
		changed = true
	}
	if changed {
		if err := Save(c); err != nil {
			return c, err
		}
	}
	return c, nil
}

// PathPrefix is the panel's base path as a URL prefix: "/x7Kq2p" or "" when the
// panel is at the root. Always without a trailing slash, so callers build
// addresses by appending.
func (c Config) PathPrefix() string {
	p := strings.Trim(strings.TrimSpace(c.BasePath), "/")
	if p == "" {
		return ""
	}
	return "/" + p
}

// URL is where this panel answers, given the host to reach it at.
func (c Config) URL(host string) string {
	return fmt.Sprintf("%s://%s:%d%s/", c.Scheme(), host, c.Port, c.PathPrefix())
}

// basePathAlphabet leaves out the characters that are misread when a URL is
// copied off a terminal by hand: no 0/O, no 1/l/I. What is left is still 57
// bits over 14 characters, which is not a space anybody sweeps.
const basePathAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// randomPathSegment returns a path segment nobody can guess.
func randomPathSegment() string {
	const n = 14
	b := make([]byte, n)
	for i := range b {
		d, err := rand.Int(rand.Reader, big.NewInt(int64(len(basePathAlphabet))))
		if err != nil {
			// Refusing to guess is the only safe answer: a predictable path is
			// worse than none, because it reads as a secret and is not one.
			return ""
		}
		b[i] = basePathAlphabet[d.Int64()]
	}
	return string(b)
}

// validBasePath reports whether a base path is one the panel can serve. A path
// segment, nothing else: no slashes, no dots, nothing that has to be escaped.
func validBasePath(s string) bool {
	s = strings.Trim(strings.TrimSpace(s), "/")
	if s == "" {
		return true // the root, which is how it is turned off
	}
	if len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
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

// RegenerateBasePath moves the panel to a new unguessable path and restarts it.
//
// The path is not a secret that has to be rotated on a schedule — it is not
// authentication and nothing is signed with it. What it is for is the case
// where it stopped being unguessable: pasted into a chat, screenshotted, typed
// on a machine that was not the operator's. Then the old one is worth throwing
// away, and there has to be a way to do it that does not involve editing JSON.
//
// The restart is what makes it take effect; the running server read its path
// once, at startup.
func RegenerateBasePath() (Config, error) {
	c := Load()
	p := randomPathSegment()
	if p == "" {
		return c, fmt.Errorf("could not generate a path")
	}
	c.BasePath = p
	if err := Save(c); err != nil {
		return c, err
	}
	manage.RestartService(app.WebUIService)
	return c, nil
}

// SetBasePath persists a path the operator chose, or "/" to put the panel back
// at the root, and restarts it.
func SetBasePath(path string) (Config, error) {
	c := Load()
	if !validBasePath(path) {
		return c, fmt.Errorf("a path is one segment of letters, digits, - and _ — " +
			"or / to serve the panel at the root")
	}
	trimmed := strings.Trim(strings.TrimSpace(path), "/")
	if trimmed == "" {
		// "/" asks for the root, and "" would be read as "none set" and
		// regenerated on the next start. They have to be told apart.
		c.BasePath = "/"
	} else {
		c.BasePath = trimmed
	}
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
# The tunnel units carry this too. A service does not inherit the ceiling in
# /etc/security/limits.conf — that file is PAM's, and applies to login sessions
# — so a unit that does not ask gets systemd's default of 1024, and no amount
# of running Optimize or rebooting changes it. This process holds the panel's
# own sockets, the node hub's listeners and whatever it proxies, so it needs
# the same headroom the tunnels were given.
LimitNOFILE=1048576

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
