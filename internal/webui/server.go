package webui

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
)

//go:embed assets/login.html
var loginHTML []byte

//go:embed assets/dashboard.html
var dashboardHTML []byte

const sessionCookie = "backpack_session"

// sessionInfo is what the panel remembers about one signed-in browser — the
// address and age make the Settings session list meaningful, and the token
// itself is never shown again.
type sessionInfo struct {
	expires time.Time
	created time.Time
	ip      string
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*sessionInfo
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: map[string]*sessionInfo{}}
}

func (s *sessionStore) create(ip string) string {
	tok := randomHex(24)
	s.mu.Lock()
	// Purge expired sessions so the map can't grow without bound over time.
	now := time.Now()
	for t, si := range s.sessions {
		if now.After(si.expires) {
			delete(s.sessions, t)
		}
	}
	s.sessions[tok] = &sessionInfo{expires: now.Add(12 * time.Hour), created: now, ip: ip}
	s.mu.Unlock()
	return tok
}

func (s *sessionStore) valid(tok string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	si, ok := s.sessions[tok]
	if !ok {
		return false
	}
	if time.Now().After(si.expires) {
		delete(s.sessions, tok)
		return false
	}
	return true
}

// sessionID is the public name of a session: a hash prefix, so the list can
// identify one without ever handing out something that logs somebody in.
func sessionID(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:6])
}

// SessionEntry is one row of the Settings session list.
type SessionEntry struct {
	ID      string `json:"id"`
	IP      string `json:"ip"`
	Created string `json:"created"`
	Current bool   `json:"current"`
}

// list returns every live session, newest first, marking the caller's own.
func (s *sessionStore) list(currentTok string) []SessionEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var out []SessionEntry
	for tok, si := range s.sessions {
		if now.After(si.expires) {
			continue
		}
		out = append(out, SessionEntry{
			ID:      sessionID(tok),
			IP:      si.ip,
			Created: si.created.Format("2006-01-02 15:04"),
			Current: tok == currentTok,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out
}

// revokeID ends the session with the given public id. Revoking your own works
// too — it is just signing out the long way around.
func (s *sessionStore) revokeID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for tok := range s.sessions {
		if sessionID(tok) == id {
			delete(s.sessions, tok)
			return
		}
	}
}

// revokeOthers ends every session except the caller's.
func (s *sessionStore) revokeOthers(currentTok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for tok := range s.sessions {
		if tok != currentTok {
			delete(s.sessions, tok)
		}
	}
}

func (s *sessionStore) destroy(tok string) {
	s.mu.Lock()
	delete(s.sessions, tok)
	s.mu.Unlock()
}

// clear invalidates every session (used after a password change).
func (s *sessionStore) clear() {
	s.mu.Lock()
	s.sessions = map[string]*sessionInfo{}
	s.mu.Unlock()
}

type server struct {
	sessions *sessionStore
}

// password always reads the current password from disk, so a change made from
// the CLI or the web UI takes effect immediately — no restart, no stale cache.
func (s *server) password() string {
	return Load().Password
}

// updatePassword persists a new password (read fresh on the next login).
func (s *server) updatePassword(pw string) error {
	c := Load()
	c.Password = pw
	return Save(c)
}

// Serve starts the web panel and blocks. Invoked by `backpack --webui`.
func Serve() error {
	cfg, err := EnsurePassword()
	if err != nil {
		return err
	}
	srv := &server{sessions: newSessionStore()}

	// The SOCKS5 relay, the watchdog, the Telegram bot and the alerts all
	// deliberately run elsewhere — in the backpack-monitor service. See
	// internal/monitor for why.

	// The panel is a monitoring dashboard: live stats, tunnel state and logs.
	// Tunnels are created and managed from the CLI; the only mutating actions
	// here are panel-scoped (password, port, self-update).
	mux := http.NewServeMux()
	mux.HandleFunc("/login", srv.handleLogin)
	mux.HandleFunc("/login2fa", srv.handleLogin2FA)
	mux.HandleFunc("/logout", srv.handleLogout)
	mux.HandleFunc("/", srv.requireAuth(srv.handleDashboard))
	// Read-only endpoints also accept the remote access token, so a peer panel
	// or a Prometheus scraper can watch without holding a browser session.
	mux.HandleFunc("/api/stats", srv.requireReadAuth(srv.handleStats))
	mux.HandleFunc("/api/tunnels", srv.requireReadAuth(srv.handleTunnels))
	mux.HandleFunc("/metrics", srv.requireReadAuth(srv.handlePrometheus))
	mux.HandleFunc("/api/logs", srv.requireAuth(srv.handleLogs))
	mux.HandleFunc("/api/password", srv.requireAuth(srv.handlePassword))
	mux.HandleFunc("/api/update", srv.requireAuth(srv.handleUpdate))
	mux.HandleFunc("/api/update/status", srv.requireAuth(srv.handleUpdateStatus))
	mux.HandleFunc("/api/panelport", srv.requireAuth(srv.handlePanelPort))
	mux.HandleFunc("/api/backup/export", srv.requireAuth(srv.handleBackupExport))
	mux.HandleFunc("/api/backup/import", srv.requireAuth(srv.handleBackupImport))
	mux.HandleFunc("/api/telegram", srv.requireAuth(srv.handleTelegram))
	mux.HandleFunc("/api/telegram/test", srv.requireAuth(srv.handleTelegramTest))
	mux.HandleFunc("/api/relays", srv.requireAuth(srv.handleRelayOptions))
	mux.HandleFunc("/api/health", srv.requireAuth(srv.handleHealth))
	mux.HandleFunc("/api/alerts", srv.requireReadAuth(srv.handleAlerts))
	mux.HandleFunc("/api/linktest", srv.requireAuth(srv.handleLinkTest))
	mux.HandleFunc("/api/restorepoints", srv.requireAuth(srv.handleRestorePoints))
	mux.HandleFunc("/api/remotetoken", srv.requireAuth(srv.handleRemoteToken))
	mux.HandleFunc("/api/security", srv.requireAuth(srv.handleSecurity))
	mux.HandleFunc("/api/sessions", srv.requireAuth(srv.handleSessions))
	mux.HandleFunc("/api/autobackup", srv.requireAuth(srv.handleAutoBackup))
	mux.HandleFunc("/api/history", srv.requireAuth(srv.handleHistory))
	mux.HandleFunc("/api/channel", srv.requireAuth(srv.handleChannel))
	// The manifest and icon are what let the panel install as an app; the
	// browser fetches them before any login, so they carry no data and no auth.
	mux.HandleFunc("/manifest.json", handleManifest)
	mux.HandleFunc("/icon.svg", handleIcon)

	addr := fmt.Sprintf("0.0.0.0:%d", cfg.Port)
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	return httpServer.ListenAndServe()
}

// requireAuth wraps a handler, redirecting unauthenticated users to /login
// (or 401 for API calls).
func (s *server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || !s.sessions.valid(c.Value) {
			if len(r.URL.Path) >= 4 && r.URL.Path[:4] == "/api" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// requireReadAuth accepts either a panel session or the remote access token.
// Only read-only endpoints go through this: the token can look, never change.
func (s *server) requireReadAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil && s.sessions.valid(c.Value) {
			next(w, r)
			return
		}
		token := Load().RemoteToken
		if token != "" {
			given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(given), []byte(token)) == 1 {
				next(w, r)
				return
			}
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		ip := clientIP(r)
		if blocked, left := limiter.blocked(ip); blocked {
			http.Error(w, fmt.Sprintf("too many failed attempts — try again in %d minutes",
				int(left.Minutes())+1), http.StatusTooManyRequests)
			return
		}
		r.ParseForm()
		given := r.FormValue("password")
		// Constant-time comparison + small delay to slow brute force.
		if subtle.ConstantTimeCompare([]byte(given), []byte(s.password())) == 1 {
			limiter.reset(ip)
			// With two-factor on, the password alone is only half the login.
			if Load().TwoFA && telegramReady() {
				if s.startTwoFA(w) {
					return
				}
				http.Error(w, "could not send the login code through Telegram — "+
					"check the bot from the CLI, or disable two_fa in the panel config file",
					http.StatusBadGateway)
				return
			}
			tok := s.sessions.create(ip)
			notifyLogin(r)
			http.SetCookie(w, &http.Cookie{
				Name:     sessionCookie,
				Value:    tok,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   12 * 3600,
			})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		limiter.fail(ip)
		time.Sleep(1 * time.Second)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(loginHTML)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(loginHTML)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.destroy(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(dashboardHTML)
}

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, GatherSystem())
}

func (s *server) handleTunnels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, GatherTunnels())
}

func (s *server) handleLogs(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(TunnelLogs(name)))
}

// handlePassword lets a logged-in user set their own password. It updates the
// running server in place (no restart) and invalidates all sessions so everyone
// must log in again with the new password.
func (s *server) handlePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.ParseForm()
	pw := strings.TrimSpace(r.FormValue("password"))
	if len(pw) < 4 || len(pw) > 128 {
		http.Error(w, "password must be 4–128 characters", http.StatusBadRequest)
		return
	}
	if err := s.updatePassword(pw); err != nil {
		http.Error(w, "could not save password", http.StatusInternalServerError)
		return
	}
	s.sessions.clear() // force re-login everywhere
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleUpdate checks for (GET) or applies (POST) a GitHub update.
// POST runs the update in the background — the panel restarts as part of it, so
// the browser should show a "reconnecting" state and reload shortly after.
func (s *server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		available, summary, err := manage.CheckUpdate()
		if err != nil {
			writeJSON(w, map[string]any{"available": false, "summary": err.Error(), "error": true})
			return
		}
		writeJSON(w, map[string]any{"available": available, "summary": summary})
	case http.MethodPost:
		updateProgress.start()
		go func() {
			err := manage.ApplyUpdate(updateProgress.log)
			updateProgress.finish(err)
		}()
		writeJSON(w, map[string]string{"status": "started"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// updateProgress records what the last update attempt did.
//
// An update can now decline to install — an archive whose checksum cannot be
// fetched or does not match is refused rather than written over the binary that
// runs every tunnel here. Discarding the log and the error, as this did, meant
// the panel showed "updating…", reloaded, and left the operator looking at the
// old version with nothing to explain why. The browser has no other channel to
// learn that: the CLI prints these lines, the panel has to fetch them.
var updateProgress = &updateRecord{}

type updateRecord struct {
	mu      sync.Mutex
	running bool
	lines   []string
	err     string
}

func (u *updateRecord) start() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.running, u.lines, u.err = true, nil, ""
}

func (u *updateRecord) log(line string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	// Bounded: a stuck update must not grow this without limit, and only the
	// tail is of any use when reading back what happened.
	if len(u.lines) >= 200 {
		u.lines = u.lines[1:]
	}
	u.lines = append(u.lines, line)
}

func (u *updateRecord) finish(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.running = false
	if err != nil {
		u.err = err.Error()
	}
}

func (u *updateRecord) snapshot() (bool, []string, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.running, append([]string(nil), u.lines...), u.err
}

// handleUpdateStatus reports the progress of a running or finished update.
func (s *server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	running, lines, errMsg := updateProgress.snapshot()
	writeJSON(w, map[string]any{"running": running, "log": lines, "error": errMsg})
}

// handlePanelPort moves the web panel itself to a new port. The response is
// sent first, then the service restarts — the browser must reconnect on the
// new port (the frontend handles the redirect).
func (s *server) handlePanelPort(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.ParseForm()
	p, err := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
	if err != nil || p < 1 || p > 65535 {
		http.Error(w, "port must be between 1 and 65535", http.StatusBadRequest)
		return
	}
	c := Load()
	if p == c.Port {
		writeJSON(w, map[string]any{"status": "ok", "port": p})
		return
	}
	if manage.PortInUse(strconv.Itoa(p)) {
		http.Error(w, fmt.Sprintf("port %d is already in use", p), http.StatusBadRequest)
		return
	}
	c.Port = p
	if err := Save(c); err != nil {
		http.Error(w, "could not save config", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"status": "ok", "port": p})
	go func() {
		time.Sleep(500 * time.Millisecond)
		manage.RestartService(app.WebUIService)
	}()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
