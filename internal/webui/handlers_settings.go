package webui

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/telegram"
)

// Settings endpoints: backup download and restore, and Telegram configuration.
//
// These are the two things people previously had to open an SSH session for.
// Everything here is admin-only by virtue of sitting behind requireAuth, and
// deliberately limited to what the CLI already offers — the panel is a
// monitoring dashboard that can now also do the two chores nobody wants to keep
// a terminal open for.

// maxBackupUpload caps a restore upload. A real backup is a few hundred
// kilobytes; anything approaching this is a mistake or an attack.
const maxBackupUpload = 32 << 20 // 32 MiB

// handleBackupExport streams a full backup as a download.
func (s *server) handleBackupExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := fmt.Sprintf("backpack-backup-%s.tar.gz", time.Now().Format("2006-01-02-1504"))

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	// The archive holds every tunnel token and the panel password, so it must
	// not be cached by the browser or any proxy in between.
	w.Header().Set("Cache-Control", "no-store")

	if err := manage.WriteBackup(w); err != nil {
		// The response has already begun, so the status cannot be changed. The
		// download will arrive truncated; saying so in the log is all that is
		// left, and the checksum-less archive will fail to extract anyway.
		return
	}
}

// handleBackupImport restores from an uploaded archive.
func (s *server) handleBackupImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBackupUpload)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, "the upload was rejected: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("backup")
	if err != nil {
		http.Error(w, "no backup file was included", http.StatusBadRequest)
		return
	}
	defer file.Close()

	res, err := manage.Restore(file)
	if err != nil {
		http.Error(w, "restore failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	// The panel's own password may have just been replaced by the one from the
	// archive, so every existing session has to go.
	s.sessions.clear()

	writeJSON(w, map[string]any{
		"status":  "ok",
		"files":   res.Files,
		"tunnels": res.Tunnels,
		"started": res.Started,
		"failed":  res.Failed,
	})
}

// telegramView is what the panel shows and edits. The token is never sent back
// to the browser in full.
type telegramView struct {
	Configured    bool   `json:"configured"`
	TokenHint     string `json:"tokenHint"`
	AdminID       string `json:"adminId"`
	IntervalHours int    `json:"intervalHours"`
	Relay         string `json:"relay"`
	RelayMode     string `json:"relayMode"` // "auto" | "direct" | tunnel name
	Alerts        struct {
		Enabled     bool `json:"enabled"`
		CPUPercent  int  `json:"cpu"`
		MemPercent  int  `json:"mem"`
		DiskPercent int  `json:"disk"`
		TunnelDown  bool `json:"tunnelDown"`
		NewRelease  bool `json:"newRelease"`
	} `json:"alerts"`
}

// handleTelegram reads (GET) or updates (POST) the bot configuration.
func (s *server) handleTelegram(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, telegramSnapshot())

	case http.MethodPost:
		if err := applyTelegramForm(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, telegramSnapshot())

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func telegramSnapshot() telegramView {
	c := telegram.Load()

	var v telegramView
	v.Configured = c.Token != "" && c.AdminID != ""
	v.TokenHint = maskToken(c.Token)
	v.AdminID = c.AdminID
	v.IntervalHours = c.IntervalHours
	v.Relay = telegram.RelayStatus()

	switch c.ViaTunnel {
	case telegram.AutoRelay:
		v.RelayMode = "auto"
	case "":
		v.RelayMode = "direct"
	default:
		v.RelayMode = c.ViaTunnel
	}

	v.Alerts.Enabled = c.Alerts.Enabled
	v.Alerts.CPUPercent = c.Alerts.CPUPercent
	v.Alerts.MemPercent = c.Alerts.MemPercent
	v.Alerts.DiskPercent = c.Alerts.DiskPercent
	v.Alerts.TunnelDown = c.Alerts.TunnelDown
	v.Alerts.NewRelease = c.Alerts.NewRelease
	return v
}

// applyTelegramForm updates the saved config from a posted form.
//
// An empty token field means "leave it alone" rather than "clear it", because
// the panel never shows the real token and so cannot send it back. Without that
// rule, saving any other setting would wipe the bot.
func applyTelegramForm(r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("could not read the form: %w", err)
	}
	c := telegram.Load()

	if tok := strings.TrimSpace(r.FormValue("token")); tok != "" {
		c.Token = tok
	}
	if admin := strings.TrimSpace(r.FormValue("adminId")); admin != "" {
		c.AdminID = admin
	}
	if c.Token == "" || c.AdminID == "" {
		return fmt.Errorf("a bot token and an admin id are both required")
	}

	if h, err := strconv.Atoi(r.FormValue("intervalHours")); err == nil && h >= 0 {
		c.IntervalHours = h
	}

	switch mode := r.FormValue("relayMode"); mode {
	case "auto":
		c.ViaTunnel, c.SocksPort = telegram.AutoRelay, 0
	case "direct":
		c.ViaTunnel, c.SocksPort = "", 0
	case "":
		// not supplied — leave as is
	default:
		port, err := manage.EnsureSocksPort(mode)
		if err != nil {
			return fmt.Errorf("could not prepare tunnel %q for relaying: %w", mode, err)
		}
		c.ViaTunnel, c.SocksPort = mode, port
	}

	c.Alerts.Enabled = formBool(r, "alertsEnabled", c.Alerts.Enabled)
	c.Alerts.TunnelDown = formBool(r, "alertTunnelDown", c.Alerts.TunnelDown)
	c.Alerts.NewRelease = formBool(r, "alertNewRelease", c.Alerts.NewRelease)
	c.Alerts.CPUPercent = formPercent(r, "alertCPU", c.Alerts.CPUPercent)
	c.Alerts.MemPercent = formPercent(r, "alertMem", c.Alerts.MemPercent)
	c.Alerts.DiskPercent = formPercent(r, "alertDisk", c.Alerts.DiskPercent)

	// Configure also (re)schedules the periodic report.
	return telegram.Configure(c)
}

// handleTelegramTest sends a test message so the user finds out here, rather
// than by waiting to see whether anything ever arrives.
func (s *server) handleTelegramTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := telegram.SendStatusNow(); err != nil {
		writeJSON(w, map[string]string{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleRelayOptions lists the tunnels that can carry the bot.
func (s *server) handleRelayOptions(w http.ResponseWriter, r *http.Request) {
	type opt struct {
		Name      string `json:"name"`
		Transport string `json:"transport"`
		State     string `json:"state"`
	}
	health := manage.AllHealth()

	out := []opt{}
	for _, t := range manage.List() {
		if t.Role != "server" {
			continue // only the server side exposes the relay port
		}
		out = append(out, opt{Name: t.Name, Transport: t.Transport, State: health[t.Name].State})
	}
	writeJSON(w, out)
}

// maskToken renders a bot token as a hint rather than a secret, so the panel
// can show that one is configured without handing it back out.
func maskToken(tok string) string {
	if tok == "" {
		return ""
	}
	// A bot token looks like 123456789:AA... — the numeric id is not secret,
	// the part after the colon is.
	if i := strings.Index(tok, ":"); i > 0 && i < len(tok)-4 {
		return tok[:i] + ":" + strings.Repeat("•", 8)
	}
	if len(tok) <= 4 {
		return strings.Repeat("•", len(tok))
	}
	return strings.Repeat("•", 8) + tok[len(tok)-4:]
}

func formBool(r *http.Request, key string, current bool) bool {
	v := r.FormValue(key)
	if v == "" {
		return current
	}
	return v == "1" || v == "true" || v == "on"
}

// formPercent reads a 0–100 threshold; 0 disables the check.
func formPercent(r *http.Request, key string, current int) int {
	v := strings.TrimSpace(r.FormValue(key))
	if v == "" {
		return current
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 100 {
		return current
	}
	return n
}
