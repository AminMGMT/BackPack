package webui

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// Who is allowed to do what, decided in one place.
//
// The panel had one password and one level of access. Anyone who could open it
// could change anything, and nothing was written down afterwards — so "who
// restarted the tunnel at 3am" had no answer, and there was no way to give
// somebody the ability to look without also giving them the ability to act.
//
// The Telegram bot already had the right model and had had it for a while:
// ReadOnly on an admin, enforced at one function, canWrite, which every action
// asks. That vocabulary is ported here rather than a second one invented,
// because two permission models in one product is how a gap appears between
// them.
//
// Three things live here and they are deliberately one file:
//
//   - scope: what a request is allowed to do;
//   - tokens: a credential for callers that are not browsers, scoped the same
//     way — which is also what puts /metrics back within reach of a scraper;
//   - the audit record, written by the same choke point that grants access, so
//     an action cannot be permitted without being recorded.
//
// The last point is the design. An audit log written by each handler is a log
// with holes in it, one per handler somebody forgot. Written by the guard, the
// only way to skip the record is to skip the authorisation.

// Scope is what a caller may do. The order matters: a larger scope includes a
// smaller one.
type Scope int

const (
	// ScopeRead may look at anything: status, metrics, logs, the fleet.
	ScopeRead Scope = iota
	// ScopeWrite may change things: create and edit tunnels, restart services,
	// upgrade servers.
	ScopeWrite
	// ScopeAdmin may change who has access, which is separate from write
	// because handing out credentials is not the same act as using them.
	ScopeAdmin
)

func (s Scope) String() string {
	switch s {
	case ScopeWrite:
		return "write"
	case ScopeAdmin:
		return "admin"
	}
	return "read"
}

// ParseScope reads a scope as an operator writes it.
func ParseScope(text string) (Scope, error) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "read", "readonly", "read-only", "ro":
		return ScopeRead, nil
	case "write", "rw":
		return ScopeWrite, nil
	case "admin", "owner":
		return ScopeAdmin, nil
	}
	return ScopeRead, fmt.Errorf("unknown scope %q — use read, write or admin", text)
}

// APIToken is a credential for a caller that is not a browser.
//
// The panel used to have one of these for read-only access and it was removed,
// correctly: it had to be minted by hand from a screen almost nobody opened,
// nothing issued it, and a credential nobody issues is a credential nobody
// rotates. The difference now is that it is scoped like everything else, it is
// listed where an operator will see it, and it has an expiry — so the reason it
// was removed does not come back.
//
// Only the hash is stored. A token that can be read back out of the panel is a
// token that leaks with a backup.
type APIToken struct {
	// Name is what it is for, in the operator's words: "prometheus", "the
	// status page". Required, because a list of anonymous tokens is a list
	// nobody will ever prune.
	Name string `json:"name"`

	// Hash is SHA-256 of the secret, hex encoded. The secret itself is shown
	// once, when it is created, and never again.
	Hash string `json:"hash"`

	Scope   Scope `json:"scope"`
	Created int64 `json:"created"`

	// Expires is when it stops working, as unix seconds. Never zero: a
	// credential with no expiry is one that outlives whatever it was issued
	// for, which is how the last one ended up unrotated.
	Expires int64 `json:"expires"`

	// LastUsed answers the only question worth asking about an old token,
	// which is whether anything still needs it.
	LastUsed int64 `json:"lastUsed,omitempty"`
}

// Expired reports whether this token is past its date.
func (t APIToken) Expired(now time.Time) bool { return now.Unix() >= t.Expires }

// tokenStore is the persisted set.
type tokenStore struct {
	Tokens []APIToken `json:"tokens,omitempty"`
}

var (
	tokensMu sync.Mutex
	// TokensPath is where the set lives. A variable rather than a constant so
	// a test can point it somewhere harmless: these tests issue and revoke
	// credentials, and they must never touch the ones on the machine running
	// them.
	TokensPath = filepath.Join(app.ConfigDir, "api-tokens.json")
)

func loadTokens() tokenStore {
	var s tokenStore
	data, err := os.ReadFile(TokensPath)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, &s); err != nil {
		// A corrupt file means every token stops working, which is the safe
		// direction and a terrible thing to discover in silence: the symptom is
		// a scraper that suddenly gets 401s, and the token list on the screen
		// says there are none, so the operator concludes somebody revoked them.
		//
		// Say so. The tokens are not recoverable from here — only their hashes
		// were ever stored — but knowing the file is damaged rather than empty
		// is the difference between reissuing one and hunting for who deleted
		// them.
		log.Printf("api tokens: %s is damaged and is being read as empty, so every "+
			"token will be refused: %v", TokensPath, err)
		return tokenStore{}
	}
	return s
}

func saveTokens(s tokenStore) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// 0600: these are credentials, even hashed. The directory is root's.
	return os.WriteFile(TokensPath, data, 0o600)
}

// hashToken is how a presented secret is compared with a stored one.
func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// IssueToken mints a token and returns the secret, which is the only time it
// exists in readable form.
func IssueToken(name string, scope Scope, ttl time.Duration) (string, APIToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", APIToken{}, fmt.Errorf("give the token a name — an unnamed token is one nobody will ever dare revoke")
	}
	if ttl <= 0 {
		return "", APIToken{}, fmt.Errorf("give the token an expiry")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", APIToken{}, fmt.Errorf("generating a token: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)

	now := time.Now()
	tok := APIToken{
		Name:    name,
		Hash:    hashToken(secret),
		Scope:   scope,
		Created: now.Unix(),
		Expires: now.Add(ttl).Unix(),
	}

	tokensMu.Lock()
	defer tokensMu.Unlock()
	s := loadTokens()
	for _, existing := range s.Tokens {
		if existing.Name == name {
			return "", APIToken{}, fmt.Errorf("there is already a token called %q", name)
		}
	}
	s.Tokens = append(s.Tokens, tok)
	if err := saveTokens(s); err != nil {
		return "", APIToken{}, err
	}
	return secret, tok, nil
}

// RevokeToken removes one by name.
func RevokeToken(name string) error {
	tokensMu.Lock()
	defer tokensMu.Unlock()
	s := loadTokens()
	out := s.Tokens[:0]
	found := false
	for _, t := range s.Tokens {
		if t.Name == name {
			found = true
			continue
		}
		out = append(out, t)
	}
	if !found {
		return fmt.Errorf("no token called %q", name)
	}
	s.Tokens = out
	return saveTokens(s)
}

// ListTokens returns what exists, never the secrets.
func ListTokens() []APIToken {
	tokensMu.Lock()
	defer tokensMu.Unlock()
	s := loadTokens()
	sort.Slice(s.Tokens, func(i, j int) bool { return s.Tokens[i].Name < s.Tokens[j].Name })
	return s.Tokens
}

// checkToken resolves a presented secret to a scope.
//
// The comparison is constant time against every stored hash, and it keeps
// going after a match: returning early on the first hit would make the time
// taken depend on where in the list the token sits, which is a way to learn how
// many tokens exist and roughly where one is.
func checkToken(secret string) (APIToken, bool) {
	if secret == "" {
		return APIToken{}, false
	}
	want := hashToken(secret)

	tokensMu.Lock()
	s := loadTokens()
	tokensMu.Unlock()

	now := time.Now()
	var found APIToken
	ok := false
	for _, t := range s.Tokens {
		if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(want)) != 1 {
			continue
		}
		if t.Expired(now) {
			continue
		}
		found, ok = t, true
	}
	if ok {
		noteTokenUse(found.Name, now)
	}
	return found, ok
}

// noteTokenUse records that a token was used, at most once a minute.
//
// A scraper hits /metrics every fifteen seconds, and rewriting the file on
// every request would turn a read into a write four times a minute for ever.
// The question the field answers — "does anything still use this" — is not made
// worse by a minute's resolution.
func noteTokenUse(name string, now time.Time) {
	tokensMu.Lock()
	defer tokensMu.Unlock()
	s := loadTokens()
	for i := range s.Tokens {
		if s.Tokens[i].Name != name {
			continue
		}
		if now.Unix()-s.Tokens[i].LastUsed < 60 {
			return
		}
		s.Tokens[i].LastUsed = now.Unix()
		_ = saveTokens(s)
		return
	}
}

// bearer pulls a token out of the request, from the Authorization header or,
// for a scraper that cannot set one, a query parameter.
func bearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if after, found := strings.CutPrefix(h, "Bearer "); found {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// caller is who is making a request, once the guard has worked it out.
type caller struct {
	// Kind is "session" or "token".
	Kind  string
	Name  string // the token's name; empty for a session
	Scope Scope
	IP    string
}

func (c caller) describe() string {
	if c.Kind == "token" {
		return "token " + c.Name
	}
	return "the panel"
}
