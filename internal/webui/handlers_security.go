package webui

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/telegram"
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
)

type loginLimiter struct {
	mu    sync.Mutex
	fails map[string]int
	until map[string]time.Time
}

var limiter = &loginLimiter{fails: map[string]int{}, until: map[string]time.Time{}}

// blocked reports whether ip is currently locked out, and for how much longer.
func (l *loginLimiter) blocked(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t, ok := l.until[ip]; ok {
		if left := time.Until(t); left > 0 {
			return true, left
		}
		delete(l.until, ip)
		delete(l.fails, ip)
	}
	return false, 0
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[ip]++
	if l.fails[ip] >= loginMaxFails {
		l.until[ip] = time.Now().Add(loginBlockPeriod)
	}
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
	delete(l.until, ip)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// --- two-factor login --------------------------------------------------------

// After a correct password, a six-digit code goes to the admin through the
// Telegram bot, and the login finishes only when it comes back. The panel and
// the bot already trust the same person, so the bot is the second factor that
// costs nothing to set up.

const (
	twoFACodeTTL     = 5 * time.Minute
	twoFAMaxAttempts = 3
)

type pendingLogin struct {
	code     string
	expires  time.Time
	attempts int
}

type twoFAStore struct {
	mu      sync.Mutex
	pending map[string]*pendingLogin
}

var twoFA = &twoFAStore{pending: map[string]*pendingLogin{}}

// start creates a pending login and returns its token and the code to send.
func (t *twoFAStore) start() (token, code string) {
	token, code = randomHex(24), randomDigits(6)
	t.mu.Lock()
	// Purge whatever expired, so abandoned logins cannot pile up.
	now := time.Now()
	for k, p := range t.pending {
		if now.After(p.expires) {
			delete(t.pending, k)
		}
	}
	t.pending[token] = &pendingLogin{code: code, expires: now.Add(twoFACodeTTL)}
	t.mu.Unlock()
	return token, code
}

// verify consumes one attempt. ok means the login completes; dead means the
// pending login is gone (expired or too many tries) and the user starts over.
func (t *twoFAStore) verify(token, given string) (ok, dead bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, found := t.pending[token]
	if !found || time.Now().After(p.expires) {
		delete(t.pending, token)
		return false, true
	}
	if subtle.ConstantTimeCompare([]byte(given), []byte(p.code)) == 1 {
		delete(t.pending, token)
		return true, false
	}
	p.attempts++
	if p.attempts >= twoFAMaxAttempts {
		delete(t.pending, token)
		return false, true
	}
	return false, false
}

func (t *twoFAStore) cancel(token string) {
	t.mu.Lock()
	delete(t.pending, token)
	t.mu.Unlock()
}

const pendingCookie = "backpack_pending"

func telegramReady() bool {
	c := telegram.Load()
	return c.Token != "" && c.AdminID != ""
}

// startTwoFA sends the code and serves the code-entry page. Returns false if
// the code could not be delivered — the caller decides what to show.
func (s *server) startTwoFA(w http.ResponseWriter) bool {
	token, code := twoFA.start()
	err := telegram.SendToAdmin("🔐 Backpack panel login code: " + code +
		"\n\nValid for 5 minutes. If this wasn't you, someone has your panel password — change it now.")
	if err != nil {
		twoFA.cancel(token)
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name: pendingCookie, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(twoFACodeTTL / time.Second),
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(codePage("")))
	return true
}

func (s *server) handleLogin2FA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	c, err := r.Cookie(pendingCookie)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	ok, dead := twoFA.verify(c.Value, strings.TrimSpace(r.FormValue("code")))
	switch {
	case ok:
		http.SetCookie(w, &http.Cookie{Name: pendingCookie, Value: "", Path: "/", MaxAge: -1})
		tok := s.sessions.create(clientIP(r))
		notifyLogin(r)
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookie, Value: tok, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 12 * 3600,
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	case dead:
		http.SetCookie(w, &http.Cookie{Name: pendingCookie, Value: "", Path: "/", MaxAge: -1})
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	default:
		time.Sleep(1 * time.Second)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(codePage("Wrong code — check Telegram and try again.")))
	}
}

// codePage is the minimal second step of the login, in the panel's own theme.
func codePage(errMsg string) string {
	msg := ""
	if errMsg != "" {
		msg = `<p class="err">` + errMsg + `</p>`
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>Backpack · Code</title>
<style>
  body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Inter,system-ui,sans-serif;
    background:#0a0a0b;color:#f5f5f7;min-height:100dvh;display:grid;place-items:center;margin:0}
  .card{width:min(92vw,360px);padding:38px 32px;background:rgba(255,255,255,.045);
    border:1px solid rgba(255,255,255,.09);border-radius:26px;text-align:center}
  h1{font-size:18px;margin:0 0 6px}
  p{font-size:13px;color:#9a9aa0;margin:0 0 20px}
  p.err{color:#ff8d85;margin:-8px 0 14px}
  input{width:100%;box-sizing:border-box;padding:14px;font-size:22px;letter-spacing:.4em;text-align:center;
    color:#f5f5f7;background:rgba(255,255,255,.05);border:1px solid rgba(255,255,255,.09);
    border-radius:12px;outline:none;font-variant-numeric:tabular-nums}
  input:focus{border-color:rgb(255,69,58)}
  button{width:100%;margin-top:14px;padding:14px;font-size:14px;font-weight:600;color:#fff;border:none;
    border-radius:12px;cursor:pointer;background:linear-gradient(145deg,rgb(255,69,58),#a12219)}
  a{display:block;margin-top:14px;font-size:12px;color:#9a9aa0}
</style></head><body>
<form class="card" method="post" action="/login2fa">
  <h1>Check Telegram</h1>
  <p>A 6-digit code was sent to the bot. It expires in 5 minutes.</p>` + msg + `
  <input name="code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" autofocus
         aria-label="Login code" placeholder="••••••">
  <button type="submit">Sign in</button>
  <a href="/login">Start over</a>
</form></body></html>`
}

// handleSecurity reads (GET) or updates (POST) the security settings.
func (s *server) handleSecurity(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c := Load()
		writeJSON(w, map[string]any{
			"twoFA": c.TwoFA, "loginNotify": c.LoginNotify,
			"telegramReady": telegramReady(),
		})
	case http.MethodPost:
		r.ParseForm()
		twoFAOn := r.FormValue("twoFA") == "1"
		notifyOn := r.FormValue("loginNotify") == "1"
		if (twoFAOn || notifyOn) && !telegramReady() {
			http.Error(w, "configure the Telegram bot first — it is what delivers the messages", http.StatusBadRequest)
			return
		}
		c := Load()
		c.TwoFA, c.LoginNotify = twoFAOn, notifyOn
		if err := Save(c); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"twoFA": twoFAOn, "loginNotify": notifyOn})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// notifyLogin tells the admin a login just happened. Fire-and-forget in the
// background: the message must never slow the login down.
func notifyLogin(r *http.Request) {
	if !Load().LoginNotify || !telegramReady() {
		return
	}
	ip := clientIP(r)
	go telegram.SendToAdmin("🔓 Panel login from " + ip + " at " +
		time.Now().Format("2006-01-02 15:04") +
		"\n\nNot you? Change the panel password now.")
}

// handleSessions lists (GET) or revokes (POST) signed-in browser sessions.
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
