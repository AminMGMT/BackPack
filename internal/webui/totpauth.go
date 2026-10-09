package webui

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// The half of the login that happens after the password.
//
// A second factor needs a state between "the password was right" and "you are
// signed in", and that state is a credential of its own: whatever carries it
// can be replayed, so it has to be short-lived, bound to the address that
// earned it, and good for nothing except answering the code prompt.
//
// It is deliberately not a session with a flag on it. A flag is one `if` away
// from being forgotten, and the thing it would be guarding is every handler in
// the panel. A separate store cannot be mistaken for a session by any code that
// was written before this existed.

// twoFactorCookie carries the half-authenticated state.
const twoFactorCookie = "backpack_2fa"

// twoFactorTTL is how long the code prompt stays open. Long enough to unlock a
// phone and read six digits, short enough that a pending token left on a shared
// machine is worth nothing by the time anybody finds it.
const twoFactorTTL = 3 * time.Minute

// pendingMaxFails is how many wrong codes one pending sign-in survives.
const pendingMaxFails = 3

type pendingEntry struct {
	ip          string
	expires     time.Time
	fails       int // code attempts reserved against this sign-in so far
	credentials string
}

// pendingStore holds the tokens that have passed the password and not the code.
type pendingStore struct {
	mu      sync.Mutex
	entries map[string]pendingEntry
}

func newPendingStore() *pendingStore {
	return &pendingStore{entries: map[string]pendingEntry{}}
}

func (p *pendingStore) create(ip string, credentials ...string) string {
	fingerprint := authFingerprint()
	if len(credentials) > 0 {
		fingerprint = credentials[0]
	}
	tok := randomHex(24)
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for t, e := range p.entries {
		if now.After(e.expires) {
			delete(p.entries, t)
		}
	}
	p.entries[tok] = pendingEntry{ip: ip, expires: now.Add(twoFactorTTL), credentials: fingerprint}
	return tok
}

// valid reports whether tok is a live pending token for ip.
//
// The address is checked as well as the token because the token travels in a
// cookie and the whole point of this state is that it is weaker than a session:
// a token that could be presented from anywhere would be a password bypass with
// a three-minute window.
func (p *pendingStore) valid(tok, ip string) bool {
	fingerprint := authFingerprint()
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[tok]
	if !ok {
		return false
	}
	if time.Now().After(e.expires) || e.credentials != fingerprint {
		delete(p.entries, tok)
		return false
	}
	return e.ip == ip && e.fails < pendingMaxFails
}

// attempt reserves one code attempt against a pending sign-in and reports
// whether it may be checked. At most pendingMaxFails are ever reserved, in one
// step with the validity check, so parallel posts cannot run more guesses than
// that against one password entry; a sign-in whose attempts are used up has
// ended (valid reports false), and a right code destroys it.
func (p *pendingStore) attempt(tok, ip string) bool {
	fingerprint := authFingerprint()
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[tok]
	if !ok || e.ip != ip || time.Now().After(e.expires) || e.fails >= pendingMaxFails || e.credentials != fingerprint {
		return false
	}
	e.fails++
	p.entries[tok] = e
	return true
}

func (p *pendingStore) destroy(tok string) {
	p.mu.Lock()
	delete(p.entries, tok)
	p.mu.Unlock()
}

// reserved rechecks a previously reserved attempt after it obtains the auth
// lock. The last allowed attempt is still usable, but a revoked token is not.
func (p *pendingStore) reserved(tok, ip string) bool {
	fingerprint := authFingerprint()
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[tok]
	return ok && e.ip == ip && time.Now().Before(e.expires) && e.fails > 0 &&
		e.fails <= pendingMaxFails && e.credentials == fingerprint
}

func (p *pendingStore) clear() {
	p.mu.Lock()
	p.entries = map[string]pendingEntry{}
	p.mu.Unlock()
}

func credentialFingerprint(c Config) string {
	sum := sha256.Sum256([]byte(c.Password + "\x00" + c.TOTPSecret))
	return hex.EncodeToString(sum[:])
}

func authFingerprint() string {
	c, err := readConfig()
	if err != nil {
		return ""
	}
	return credentialFingerprint(c)
}

// twoFactorOn reports whether this panel demands a code.
func twoFactorOn() bool { return Load().TOTPSecret != "" }

// checkSecondFactor accepts either a current code or an unused recovery code,
// and spends the recovery code if that is what it was.
//
// Both are checked because an operator reaching for a recovery code is an
// operator who has already lost the phone; telling them to use a different
// field for it is one more thing to get wrong while locked out.
func checkSecondFactor(given string) bool {
	accepted := false
	_, err := UpdateConfig(func(c *Config) error {
		if c.TOTPSecret == "" {
			return nil
		}
		if totpValid(c.TOTPSecret, given, time.Now()) {
			accepted = true
			return nil
		}
		remaining, ok := useRecoveryCode(c.RecoveryHashes, given)
		if ok {
			c.RecoveryHashes = remaining
			accepted = true
		}
		return nil
	})
	return accepted && err == nil
}

// TwoFactorStatus is what the Security pane reads.
type TwoFactorStatus struct {
	// Enabled is whether a code is demanded at login.
	Enabled bool `json:"enabled"`
	// RecoveryLeft is how many single-use codes are unspent. An operator with
	// none left has a phone and nothing else.
	RecoveryLeft int `json:"recoveryLeft"`
	// Secret and URI are filled in only while enrolling, and only for the
	// caller who asked to enrol.
	Secret string `json:"secret,omitempty"`
	URI    string `json:"uri,omitempty"`
}

// handleTOTP is the Security pane's two-factor control.
//
//	GET               the current state
//	POST start        begin enrolment: a secret to show, not yet in force
//	POST confirm      a code proves the app has the secret; turn it on
//	POST disable      the password again, and it is off
//	POST recovery     fresh recovery codes, shown once
func (s *server) handleTOTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		c := Load()
		writeJSON(w, TwoFactorStatus{
			Enabled:      c.TOTPSecret != "",
			RecoveryLeft: len(c.RecoveryHashes),
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	s.authMu.Lock()
	defer s.authMu.Unlock()
	switch r.FormValue("action") {
	case "start":
		secret, err := newTOTPSecret()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.enrolMu.Lock()
		s.enrolling = secret
		s.enrolMu.Unlock()
		host, _ := os.Hostname()
		writeJSON(w, TwoFactorStatus{Secret: secret, URI: otpauthURI(secret, host)})

	case "confirm":
		s.enrolMu.Lock()
		secret := s.enrolling
		s.enrolMu.Unlock()
		if secret == "" {
			http.Error(w, "start the setup again — the secret was not kept", http.StatusConflict)
			return
		}
		// The code is what proves the app actually holds the secret. Turning
		// two-factor on without it is how an operator locks themselves out with
		// a QR code they never scanned.
		if !totpValid(secret, r.FormValue("code"), time.Now()) {
			http.Error(w, "that code does not match — check the clock on the phone", http.StatusBadRequest)
			return
		}
		codes, hashes, err := newRecoveryCodes()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, err = UpdateConfig(func(c *Config) error { c.TOTPSecret = secret; c.RecoveryHashes = hashes; return nil })
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.enrolMu.Lock()
		s.enrolling = ""
		s.enrolMu.Unlock()
		s.pending.clear()
		writeJSON(w, map[string]any{"enabled": true, "recovery": codes})

	case "disable":
		// The password again, because turning the second factor off is the one
		// action that undoes the second factor — a stolen session should not be
		// able to do it quietly.
		if !passwordMatches(r.FormValue("password")) {
			http.Error(w, "that is not the panel password", http.StatusForbidden)
			return
		}
		_, err := UpdateConfig(func(c *Config) error { c.TOTPSecret = ""; c.RecoveryHashes = nil; return nil })
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.pending.clear()
		writeJSON(w, map[string]any{"enabled": false})

	case "recovery":
		if !passwordMatches(r.FormValue("password")) {
			http.Error(w, "that is not the panel password", http.StatusForbidden)
			return
		}
		c := Load()
		if c.TOTPSecret == "" {
			http.Error(w, "two-factor is not on", http.StatusConflict)
			return
		}
		codes, hashes, err := newRecoveryCodes()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, err = UpdateConfig(func(current *Config) error {
			if current.TOTPSecret != c.TOTPSecret {
				return fmt.Errorf("two-factor settings changed; try again")
			}
			current.RecoveryHashes = hashes
			return nil
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"recovery": codes})

	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// passwordMatches compares a given password with the panel's, in constant time.
func passwordMatches(given string) bool {
	c, err := readConfig()
	return err == nil && c.Password != "" && subtle.ConstantTimeCompare([]byte(given), []byte(c.Password)) == 1
}

// TwoFactorEnabled reports whether the panel demands a code. It is exported for
// the CLI, which is the way back in when the phone is gone and the recovery
// codes are gone with it.
func TwoFactorEnabled() bool { return twoFactorOn() }

// DisableTwoFactor turns the second factor off from the machine itself.
//
// There is no password check here and that is deliberate: this runs on the
// server, as root, from a terminal somebody already has. Anybody who can call
// it can read the configuration file the secret is in, and could edit it by
// hand — asking for the panel password would protect nothing and would leave an
// operator who has forgotten it with no way back at all.
func DisableTwoFactor() error {
	_, err := UpdateConfig(func(c *Config) error { c.TOTPSecret = ""; c.RecoveryHashes = nil; return nil })
	return err
}

// RecoveryCodesLeft is how many single-use codes are unspent.
func RecoveryCodesLeft() int { return len(Load().RecoveryHashes) }
