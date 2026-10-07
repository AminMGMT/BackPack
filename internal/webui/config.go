// Package webui serves an authenticated, dark-themed web dashboard on port
// 7777 showing live system metrics, tunnels and their logs.
package webui

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

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
	// TOTPSecret is the shared secret of the panel's second factor, base32 as
	// an authenticator app writes it. Empty means no second factor, which is
	// the default and what every panel has until somebody enrols one.
	//
	// It is kept in the same file as the password because it protects the same
	// thing and travels in the same backup; neither is a secret the file can
	// keep from anybody who can read it, which is why that file is 0600 and why
	// a backup of it is treated as a credential. See totp.go.
	TOTPSecret string `json:"totp_secret,omitempty"`

	// Stopped records that the operator stopped the panel (Web Panel → Stop
	// panel), so nothing starts it again behind their back.
	//
	// Every run of the menu used to start the panel on its way in, and stopping
	// it left no record beyond the missing unit — so the next `sudo backpack`
	// wrote the unit and started it again. Reported against v1.8.4 as "I turn
	// the web panel off by hand, and every time I run the script it turns it
	// back on". Only an explicit start (Restart panel) clears it.
	Stopped bool `json:"stopped,omitempty"`

	// RecoveryHashes are the SHA-256 hashes of the single-use codes issued with
	// the secret. Hashed because the codes are the way back in when the phone
	// is gone, and a file that lists them is a file that is the second factor.
	RecoveryHashes []string `json:"recovery_hashes,omitempty"`

	// TLSSelfHost is an optional domain or IP to add to the self-signed
	// certificate's SANs, for reaching the panel by a name that has no public
	// DNS for Let's Encrypt (an internal domain, a host that is only in the
	// operator's /etc/hosts). It changes nothing about which addresses already
	// work — every local IP and loopback are always included — it only adds one
	// the machine cannot discover on its own. Empty is the common case.
	TLSSelfHost string `json:"tls_self_host,omitempty"`

	// TLSCertFile and TLSKeyFile are a certificate the operator brings: PEM,
	// the full chain in one file and its private key in the other — what
	// certbot writes as fullchain.pem and privkey.pem. Set, they are served
	// as they are, in place of Let's Encrypt and the self-signed pair, and
	// re-read whenever the certificate file changes, so a renewal lands
	// without a restart. (#49: a panel whose server cannot pass Let's
	// Encrypt's check from here had no way to use a certificate obtained any
	// other way — a copied-in one was overwritten by the self-signed pair,
	// which did not name this server's addresses.)
	TLSCertFile string `json:"tls_cert,omitempty"`
	TLSKeyFile  string `json:"tls_key,omitempty"`
}

// OwnCert reports whether the panel serves a certificate the operator brought.
func (c Config) OwnCert() bool { return c.TLSCertFile != "" && c.TLSKeyFile != "" }

// Equal reports whether two configurations say the same thing.
//
// It exists because Config grew a slice — the recovery-code hashes — and a
// struct holding one cannot be compared with ==. The compiler catches that,
// which is the good case; what it would not catch is somebody later adding a
// field and forgetting it here, so this compares every field explicitly and the
// test beside it fails when the count changes.
func (c Config) Equal(other Config) bool {
	if len(c.RecoveryHashes) != len(other.RecoveryHashes) {
		return false
	}
	for i := range c.RecoveryHashes {
		if c.RecoveryHashes[i] != other.RecoveryHashes[i] {
			return false
		}
	}
	return c.Password == other.Password &&
		c.Stopped == other.Stopped &&
		c.Port == other.Port &&
		c.BasePath == other.BasePath &&
		c.HTTPS == other.HTTPS &&
		c.TLSDomain == other.TLSDomain &&
		c.TLSEmail == other.TLSEmail &&
		c.TLSSelfHost == other.TLSSelfHost &&
		c.TLSCertFile == other.TLSCertFile &&
		c.TLSKeyFile == other.TLSKeyFile &&
		c.TOTPSecret == other.TOTPSecret
}

// Scheme is the URL scheme the panel answers on.
func (c Config) Scheme() string {
	if c.HTTPS {
		return "https"
	}
	return "http"
}

// configOverride redirects the panel's configuration file.
//
// It exists for one reason and it is a good one: the login flow is the most
// security-relevant code in this package and it was untestable, because Load
// and Save name a path under /etc that a test process cannot write. Every
// assertion about what a wrong password does, or what a second factor demands,
// needed a config file the test could control.
//
// It is unexported and set only by useConfigFile, which lives in the test file.
// Nothing in a running panel can move it.
var configOverride string

func configPath() string {
	if configOverride != "" {
		return configOverride
	}
	return app.WebUIConfig
}

// Load reads the saved config, filling defaults for missing fields.
func Load() Config {
	var c Config
	// Unreadable means set aside, not overwritten: the password, the second
	// factor and the access tokens are in it. EnsurePassword then starts a
	// fresh one — closed, with a new password — rather than an open panel.
	app.WarnState(withConfigLock(func() error { return app.LoadState(configPath(), &c) }))
	if c.Port == 0 {
		c.Port = app.WebUIPort
	}
	return c
}

var configMu sync.Mutex

// withConfigLock protects complete updates, including CLI processes, rather
// than only the atomic replacement of their final JSON files.
func withConfigLock(work func() error) error {
	configMu.Lock()
	defer configMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(configPath()), 0755); err != nil {
		return err
	}
	lock, err := os.OpenFile(configPath()+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return work()
}

func readConfig() (Config, error) {
	var c Config
	b, err := os.ReadFile(configPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return c, err
	}
	if err == nil {
		if err := json.Unmarshal(b, &c); err != nil {
			return Config{}, err
		}
	}
	if c.Port == 0 {
		c.Port = app.WebUIPort
	}
	return c, nil
}

// UpdateConfig changes only fields owned by the caller, without reviving a
// recovery code consumed by another request between Load and Save.
func UpdateConfig(change func(*Config) error) (c Config, err error) {
	err = withConfigLock(func() error {
		var err error
		c, err = readConfig()
		if err != nil {
			return err
		}
		before := c
		before.RecoveryHashes = append([]string(nil), c.RecoveryHashes...)
		if err := change(&c); err != nil {
			return err
		}
		if c.Equal(before) {
			return nil
		}
		return saveConfig(c)
	})
	return c, err
}

// Save persists a complete config (0600, root only). Incremental writers use
// UpdateConfig to preserve fields owned by other callers.
func Save(c Config) error {
	return withConfigLock(func() error { return saveConfig(c) })
}

func saveConfig(c Config) error {
	data, _ := json.MarshalIndent(c, "", "  ")
	// Atomic: the panel reads this on every login and the CLI shows the password
	// from it, so a truncated read would look like a wrong password.
	return app.WriteFileAtomic(configPath(), data, 0600)
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
	// Preserve the startup recovery/warning path; runtime authentication uses
	// strict reads and never turns absent credentials into a valid login.
	Load()
	return UpdateConfig(func(c *Config) error {
		if c.Password == "" {
			c.Password = randomDigits(8)
		}
		if c.BasePath == "" {
			c.BasePath = randomPathSegment()
		}
		return nil
	})
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
	c, err := UpdateConfig(func(c *Config) error { c.Password = randomDigits(8); return nil })
	if err != nil {
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
	path := randomPathSegment()
	if path == "" {
		return Load(), fmt.Errorf("could not generate a path")
	}
	c, err := UpdateConfig(func(c *Config) error { c.BasePath = path; return nil })
	if err != nil {
		return c, err
	}
	manage.RestartService(app.WebUIService)
	return c, nil
}

// SetBasePath persists a path the operator chose, or "/" to put the panel back
// at the root, and restarts it.
func SetBasePath(path string) (Config, error) {
	if !validBasePath(path) {
		return Load(), fmt.Errorf("a path is one segment of letters, digits, - and _ — or / to serve the panel at the root")
	}
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		path = "/"
	}
	c, err := UpdateConfig(func(c *Config) error { c.BasePath = path; return nil })
	if err != nil {
		return c, err
	}
	manage.RestartService(app.WebUIService)
	return c, nil
}

// SetPassword persists a custom password and restarts the panel service so the
// change takes effect. Used from the CLI (a separate process from the server).
func SetPassword(pw string) (Config, error) {
	c, err := UpdateConfig(func(c *Config) error { c.Password = pw; return nil })
	if err != nil {
		return c, err
	}
	manage.RestartService(app.WebUIService)
	return c, nil
}

// SetPort persists a new panel port and restarts the panel service so it
// listens there. Used from the CLI (a separate process from the server).
func SetPort(port int) (Config, error) {
	if port < 1 || port > 65535 {
		return Load(), fmt.Errorf("port must be between 1 and 65535")
	}
	c, err := UpdateConfig(func(c *Config) error { c.Port = port; return nil })
	if err != nil {
		return c, err
	}
	manage.RestartService(app.WebUIService)
	return c, nil
}

// EnsureRunning makes sure a password exists and the web-panel systemd service
// is installed and running. Safe to call repeatedly (idempotent).
//
// It is the explicit start — Restart panel, a restore of a panel that was
// running — so it also clears a stop the operator made earlier. The menu's own
// start on every run is StartUnlessStopped.
func EnsureRunning() (Config, error) {
	c, err := EnsurePassword()
	if err != nil {
		return c, err
	}
	if c.Stopped {
		c, err = UpdateConfig(func(c *Config) error { c.Stopped = false; return nil })
		if err != nil {
			return c, err
		}
	}
	return c, installAndStart(panelUnit())
}

// StartUnlessStopped is what the menu does each time it opens: it brings the
// panel up — a new install gets one without being asked — unless the operator
// stopped it. started reports whether it did.
func StartUnlessStopped() (c Config, started bool, err error) {
	if StoppedByOperator() {
		return Load(), false, nil
	}
	c, err = EnsureRunning()
	return c, err == nil, err
}

// StoppedByOperator reports whether the panel was stopped on purpose.
//
// Before Stopped existed, stopping the panel removed its unit and wrote nothing
// else, so a panel stopped under v1.8.4 or earlier is recognised by that: its
// config exists — it was set up once — and its unit does not. A new install has
// neither, and gets a panel.
func StoppedByOperator() bool {
	if Load().Stopped {
		return true
	}
	_, cfgErr := os.Stat(configPath())
	_, unitErr := os.Stat(panelUnitPath())
	return cfgErr == nil && errors.Is(unitErr, fs.ErrNotExist)
}

// Disable stops and removes the web-panel service, and records that the
// operator wanted it stopped so the next run of the menu leaves it that way.
func Disable() error {
	if _, err := UpdateConfig(func(c *Config) error { c.Stopped = true; return nil }); err != nil {
		return err
	}
	return removeUnit()
}

// panelUnitPath is where the panel's unit file lives; a variable so a test can
// point it at a temporary directory.
var panelUnitPath = func() string { return app.ServiceDir + "/" + app.WebUIService }

// installAndStart writes the unit and starts the service, and removeUnit stops
// and removes it. Variables so a test can record what would have been done to
// systemd instead of doing it.
var installAndStart = func(unit string) error {
	if err := os.WriteFile(panelUnitPath(), []byte(unit), 0644); err != nil {
		return err
	}
	if err := manage.DaemonReload(); err != nil {
		return err
	}
	return manage.StartService(app.WebUIService)
}

var removeUnit = func() error {
	if manage.IsActive(app.WebUIService) || manage.IsEnabled(app.WebUIService) {
		manage.DisableService(app.WebUIService)
	}
	os.Remove(panelUnitPath())
	return manage.DaemonReload()
}

// panelUnit is the panel's systemd unit.
func panelUnit() string {
	return fmt.Sprintf(`[Unit]
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

// SaveTLS changes certificate settings without replacing live credentials.
func SaveTLS(next Config) error {
	_, err := UpdateConfig(func(c *Config) error {
		c.HTTPS, c.TLSDomain, c.TLSEmail, c.TLSSelfHost = next.HTTPS, next.TLSDomain, next.TLSEmail, next.TLSSelfHost
		c.TLSCertFile, c.TLSKeyFile = next.TLSCertFile, next.TLSKeyFile
		return nil
	})
	return err
}
