package webui

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/manage"
)

// --- login rate limiting -----------------------------------------------------

// A wrong password already costs a one-second delay; this adds a ceiling: five
// consecutive failures from one address block that address for ten minutes.
// The panel sits on a public port on a server whose address gets scanned, and
// an eight-digit password should not be brute-forceable just because nobody
// was watching the log.
const (
	loginMaxFails    = 5
	loginBlockPeriod = 10 * time.Minute

	// How many addresses the limiter will remember at once.
	//
	// It remembers one entry per source address and only ever forgot an
	// address that came back — so an address that failed once and never
	// returned stayed in the map for the life of the process. The panel sits
	// on a port that gets scanned, and a scan from a rotating source is the
	// ordinary case rather than the exotic one, which made a defence against
	// brute force into a way to grow the panel's memory from outside it.
	//
	// Well above the number of addresses any real panel sees, and small enough
	// that the worst case is a few hundred kilobytes.
	loginMaxTracked = 4096
)

// loginAttempt is what the limiter remembers about one address.
type loginAttempt struct {
	fails int
	until time.Time
	seen  time.Time
}

type loginLimiter struct {
	mu   sync.Mutex
	byIP map[string]*loginAttempt
}

var limiter = newLoginLimiter()

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{byIP: map[string]*loginAttempt{}}
}

// blocked reports whether ip is currently locked out, and for how much longer.
func (l *loginLimiter) blocked(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.byIP[ip]
	if !ok {
		return false, 0
	}
	if a.isBlocked(time.Now()) {
		return true, time.Until(a.until)
	}
	if !a.until.IsZero() {
		// The block ran out, so the slate is clean again.
		delete(l.byIP, ip)
	}
	return false, 0
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byIP == nil {
		l.byIP = map[string]*loginAttempt{}
	}

	a, ok := l.byIP[ip]
	if !ok {
		l.evictLocked()
		a = &loginAttempt{}
		l.byIP[ip] = a
	}
	a.fails++
	a.seen = time.Now()
	if a.fails >= loginMaxFails {
		a.until = a.seen.Add(loginBlockPeriod)
	}
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byIP, ip)
}

// isBlocked reports whether this address is inside its lockout right now.
func (a *loginAttempt) isBlocked(now time.Time) bool {
	return !a.until.IsZero() && now.Before(a.until)
}

// evictLocked makes room for a new address. Called with l.mu held, and only on
// the insert that would push the map past its bound.
//
// A blocked address is never what gets given up. Evicting by age alone looks
// reasonable and is the bypass: an address is stamped when it fails, so one
// that hit the limit and was told to wait ten minutes immediately becomes the
// least recently seen thing in the map, and a flood of fresh addresses — which
// is precisely what an attacker with a proxy pool produces — would push out
// the entry keeping that attacker out. The block would lift itself.
func (l *loginLimiter) evictLocked() {
	if len(l.byIP) < loginMaxTracked {
		return
	}
	now := time.Now()

	// Entries that are neither blocked nor recent are doing nothing at all.
	for ip, a := range l.byIP {
		if !a.isBlocked(now) && now.Sub(a.seen) > loginBlockPeriod {
			delete(l.byIP, ip)
		}
	}
	if len(l.byIP) < loginMaxTracked {
		return
	}

	// Then the least recently seen address that is not being kept out.
	var victim string
	var oldest time.Time
	for ip, a := range l.byIP {
		if a.isBlocked(now) {
			continue
		}
		if victim == "" || a.seen.Before(oldest) {
			victim, oldest = ip, a.seen
		}
	}
	if victim != "" {
		delete(l.byIP, victim)
		return
	}

	// Every address tracked is currently blocked, so one of them has to go:
	// the one whose lockout ends soonest, being the closest to expiring on its
	// own. Reaching here means thousands of distinct addresses have each spent
	// five failed attempts, and the bound on memory is the thing being
	// defended at that point.
	var soonest time.Time
	for ip, a := range l.byIP {
		if victim == "" || a.until.Before(soonest) {
			victim, soonest = ip, a.until
		}
	}
	delete(l.byIP, victim)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *server) handleSessions(w http.ResponseWriter, r *http.Request) {
	cur := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		cur = c.Value
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.sessions.list(cur))
	case http.MethodPost:
		r.ParseForm()
		switch r.FormValue("action") {
		case "revoke":
			s.sessions.revokeID(r.FormValue("id"))
		case "others":
			s.sessions.revokeOthers(cur)
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
		writeJSON(w, s.sessions.list(cur))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAutoBackup reads (GET) or switches (POST) the weekly automatic backup
// taken by the monitor service.
func (s *server) handleAutoBackup(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]bool{"enabled": manage.AutoBackupEnabled()})
	case http.MethodPost:
		r.ParseForm()
		on := r.FormValue("enabled") == "1"
		if err := manage.SetAutoBackup(on); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]bool{"enabled": on})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
