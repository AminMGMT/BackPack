package webui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/schedule"
	"github.com/backpack/backpack/internal/socks"
	"github.com/backpack/backpack/internal/telegram"
)

//go:embed assets/login.html
var loginHTML []byte

//go:embed assets/dashboard.html
var dashboardHTML []byte

const sessionCookie = "backpack_session"

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: map[string]time.Time{}}
}

func (s *sessionStore) create() string {
	tok := randomHex(24)
	s.mu.Lock()
	s.sessions[tok] = time.Now().Add(12 * time.Hour)
	s.mu.Unlock()
	return tok
}

func (s *sessionStore) valid(tok string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, tok)
		return false
	}
	return true
}

func (s *sessionStore) destroy(tok string) {
	s.mu.Lock()
	delete(s.sessions, tok)
	s.mu.Unlock()
}

// clear invalidates every session (used after a password change).
func (s *sessionStore) clear() {
	s.mu.Lock()
	s.sessions = map[string]time.Time{}
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

	// Built-in SOCKS5 proxy on localhost, authenticated by any local tunnel
	// token. When exposed over a tunnel it lets the peer (e.g. Iran) reach the
	// internet through this node (e.g. kharej) — used for Telegram relaying.
	go socks.Serve(context.Background(),
		fmt.Sprintf("127.0.0.1:%d", app.SocksInternalPort),
		func(_, pass string) bool { return manage.TokenMatches(pass) })

	// Self-healing watchdog: detect and restart dropped tunnels quickly.
	go manage.RunWatchdog(context.Background())

	// Interactive Telegram bot (Status / Web UI / Support buttons). No-op until
	// the bot is configured; runs only where it is configured (normally Iran).
	go telegram.RunBot(context.Background())

	mux := http.NewServeMux()
	mux.HandleFunc("/login", srv.handleLogin)
	mux.HandleFunc("/logout", srv.handleLogout)
	mux.HandleFunc("/", srv.requireAuth(srv.handleDashboard))
	mux.HandleFunc("/api/stats", srv.requireAuth(srv.handleStats))
	mux.HandleFunc("/api/tunnels", srv.requireAuth(srv.handleTunnels))
	mux.HandleFunc("/api/logs", srv.requireAuth(srv.handleLogs))
	mux.HandleFunc("/api/password", srv.requireAuth(srv.handlePassword))
	mux.HandleFunc("/api/update", srv.requireAuth(srv.handleUpdate))
	mux.HandleFunc("/api/schedule", srv.requireAuth(srv.handleSchedule))
	mux.HandleFunc("/api/tunnels/create", srv.requireAuth(srv.handleTunnelCreate))
	mux.HandleFunc("/api/tunnels/action", srv.requireAuth(srv.handleTunnelAction))
	mux.HandleFunc("/api/telegram", srv.requireAuth(srv.handleTelegram))
	mux.HandleFunc("/api/telegram/test", srv.requireAuth(srv.handleTelegramTest))

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

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		r.ParseForm()
		given := r.FormValue("password")
		// Constant-time comparison + small delay to slow brute force.
		if subtle.ConstantTimeCompare([]byte(given), []byte(s.password())) == 1 {
			tok := s.sessions.create()
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
		go manage.ApplyUpdate(func(string) {})
		writeJSON(w, map[string]string{"status": "started"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSchedule gets (GET) or sets (POST) the auto-refresh interval in hours.
func (s *server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]int{"hours": schedule.AutoRefreshHours()})
	case http.MethodPost:
		r.ParseForm()
		h, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("hours")))
		if h < 0 {
			h = 0
		}
		if h > 168 {
			h = 168
		}
		if err := schedule.SetAutoRefresh(h); err != nil {
			http.Error(w, "could not update schedule", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]int{"hours": schedule.AutoRefreshHours()})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleTunnelCreate creates a best-performance tunnel (web equivalent of the
// CLI "Setup Server" / "Setup Client" flows). role=server (default) generates
// and returns a token; role=client requires the server's token.
func (s *server) handleTunnelCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.ParseForm()
	name := r.FormValue("name")
	port := r.FormValue("port")
	transport := r.FormValue("transport")
	country := r.FormValue("country")

	if r.FormValue("role") == "client" {
		err := manage.CreateClientTunnel(name, r.FormValue("host"), port, transport,
			r.FormValue("token"), r.FormValue("edge_ip"), country)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"status": "created"})
		return
	}

	ipv6 := r.FormValue("ipv6") == "true" || r.FormValue("ipv6") == "1"
	ports := strings.Split(r.FormValue("ports"), ",")
	socksPort, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("socks_port")))

	token, err := manage.CreateServerTunnel(name, port, transport, ports, ipv6, country, socksPort,
		r.FormValue("tls_cert"), r.FormValue("tls_key"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"status": "created", "token": token})
}

// handleTunnelAction starts/stops/restarts/deletes a tunnel by name.
func (s *server) handleTunnelAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.ParseForm()
	if err := manage.TunnelAction(r.FormValue("name"), r.FormValue("action")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleTelegram gets (GET) or saves (POST) the Telegram bot configuration.
func (s *server) handleTelegram(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c := telegram.Load()
		writeJSON(w, map[string]any{
			"token":      c.Token,
			"admin_id":   c.AdminID,
			"interval":   c.IntervalHours,
			"via_tunnel": c.ViaTunnel,
		})
	case http.MethodPost:
		r.ParseForm()
		c := telegram.Config{
			Token:         strings.TrimSpace(r.FormValue("token")),
			AdminID:       strings.TrimSpace(r.FormValue("admin_id")),
			IntervalHours: atoiDefault(r.FormValue("interval"), 6),
			ViaTunnel:     strings.TrimSpace(r.FormValue("via_tunnel")),
		}
		if c.Token == "" || c.AdminID == "" {
			http.Error(w, "bot token and admin id are required", http.StatusBadRequest)
			return
		}
		if c.ViaTunnel != "" {
			port, err := manage.EnsureSocksPort(c.ViaTunnel)
			if err != nil {
				http.Error(w, "relay setup failed: "+err.Error(), http.StatusBadRequest)
				return
			}
			c.SocksPort = port
		}
		if err := telegram.Configure(c); err != nil {
			http.Error(w, "could not save: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleTelegramTest sends a test message with the saved configuration.
func (s *server) handleTelegramTest(w http.ResponseWriter, r *http.Request) {
	if err := telegram.SendTest(telegram.Load()); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"status": "sent"})
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n >= 0 {
		return n
	}
	return def
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
