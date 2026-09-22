package webui

import (
	"crypto/subtle"
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

type pendingEntry struct {
	ip      string
	expires time.Time
}

// pendingStore holds the tokens that have passed the password and not the code.
type pendingStore struct {
	mu      sync.Mutex
	entries map[string]pendingEntry
}

func newPendingStore() *pendingStore {
	return &pendingStore{entries: map[string]pendingEntry{}}
}

func (p *pendingStore) create(ip string) string {
	tok := randomHex(24)
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for t, e := range p.entries {
		if now.After(e.expires) {
			delete(p.entries, t)
		}
	}
	p.entries[tok] = pendingEntry{ip: ip, expires: now.Add(twoFactorTTL)}
	return tok
}

// valid reports whether tok is a live pending token for ip.
//
// The address is checked as well as the token because the token travels in a
// cookie and the whole point of this state is that it is weaker than a session:
// a token that could be presented from anywhere would be a password bypass with
// a three-minute window.
func (p *pendingStore) valid(tok, ip string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[tok]
	if !ok {
		return false
	}
	if time.Now().After(e.expires) {
		delete(p.entries, tok)
		return false
	}
	return e.ip == ip
}

func (p *pendingStore) destroy(tok string) {
	p.mu.Lock()
	delete(p.entries, tok)
	p.mu.Unlock()
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
	c := Load()
	if c.TOTPSecret == "" {
		return false
	}
	if totpValid(c.TOTPSecret, given, time.Now()) {
		return true
	}
	remaining, ok := useRecoveryCode(c.RecoveryHashes, given)
	if !ok {
		return false
	}
	c.RecoveryHashes = remaining
	// A recovery code that is spent and not recorded as spent is a code that
	// works twice, so a failed write refuses the login rather than allowing it.
	if err := Save(c); err != nil {
		return false
	}
	return true
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
		c := Load()
		c.TOTPSecret = secret
		c.RecoveryHashes = hashes
		if err := Save(c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.enrolMu.Lock()
		s.enrolling = ""
		s.enrolMu.Unlock()
		writeJSON(w, map[string]any{"enabled": true, "recovery": codes})

	case "disable":
		// The password again, because turning the second factor off is the one
		// action that undoes the second factor — a stolen session should not be
		// able to do it quietly.
		if !passwordMatches(r.FormValue("password")) {
			http.Error(w, "that is not the panel password", http.StatusForbidden)
			return
		}
		c := Load()
		c.TOTPSecret = ""
		c.RecoveryHashes = nil
		if err := Save(c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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
		c.RecoveryHashes = hashes
		if err := Save(c); err != nil {
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
	return subtle.ConstantTimeCompare([]byte(given), []byte(Load().Password)) == 1
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
	c := Load()
	c.TOTPSecret = ""
	c.RecoveryHashes = nil
	return Save(c)
}

// RecoveryCodesLeft is how many single-use codes are unspent.
func RecoveryCodesLeft() int { return len(Load().RecoveryHashes) }
