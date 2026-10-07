package webui

// What the menu's Backup & Restore, Update and Web Panel screens could do and
// the panel could not: a tunnel's setup link, backups kept on the server (and
// the drill that proves one restores), the off-site copy, installing a release
// from a file, rolling back to a restore point, and the panel's own path,
// login code and restart.
//
// Each calls what the menu calls. The one thing deliberately left out is the
// fleet key — see menu/backup.go: it is kept out of the archive and out of the
// panel on purpose, so that the two never travel together.

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
)

// --- setup link --------------------------------------------------------------

func (s *server) handleTunnelLink(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	// The name is checked where the config is read (LoadTunnelConfig).
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	info, err := manage.SetupLinkFor(name, r.URL.Query().Get("host"))
	if err != nil {
		http.Error(w, "could not build the link: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, info)
}

// --- traffic limit -------------------------------------------------------------

// handleTunnelQuota is GET ?name= for a tunnel's limit and use, and POST
// name + limit (bytes, 0 for none) to set it. The engine enforces it; see
// internal/quota.
func (s *server) handleTunnelQuota(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q, err := manage.ReadTunnelQuota(r.URL.Query().Get("name"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, q)
	case http.MethodPost:
		_ = r.ParseForm()
		limit, err := strconv.ParseUint(strings.TrimSpace(r.FormValue("limit")), 10, 64)
		if err != nil {
			http.Error(w, "the limit is a number of bytes — 0 for no limit", http.StatusBadRequest)
			return
		}
		q, err := manage.SetTunnelQuota(r.FormValue("name"), limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, q)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- backups kept on this server ---------------------------------------------

type backupFile struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

func backupFiles() []backupFile {
	paths, _ := filepath.Glob(filepath.Join(app.BackupDir, "*.tar.gz"))
	out := []backupFile{}
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			out = append(out, backupFile{Name: filepath.Base(p), Size: st.Size(), Modified: st.ModTime().Unix()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	return out
}

// backupPath resolves a name from the list to its file, and nothing else: a
// name with a separator or one that is not in the folder is refused, so the
// handlers below can never be pointed at an arbitrary path.
func backupPath(name string) (string, bool) {
	if name == "" || name != filepath.Base(name) || !strings.HasSuffix(name, ".tar.gz") {
		return "", false
	}
	p := filepath.Join(app.BackupDir, name)
	st, err := os.Stat(p)
	return p, err == nil && !st.IsDir()
}

func (s *server) handleBackups(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"dir": app.BackupDir, "files": backupFiles(),
			"offsite": manage.OffsiteCommand()})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	switch r.FormValue("action") {
	case "create":
		path, err := manage.BackupToFile(app.BackupDir)
		if err != nil {
			http.Error(w, "backup failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"created": filepath.Base(path), "files": backupFiles()})
	case "test":
		p, ok := backupPath(r.FormValue("name"))
		if !ok {
			http.Error(w, "no such backup in "+app.BackupDir, http.StatusBadRequest)
			return
		}
		rep, err := manage.TestRestore(p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{
			"name": filepath.Base(p), "files": rep.Files, "tunnels": rep.Tunnels,
			"webui": rep.WebUIConfig, "telegram": rep.TelegramConfig,
			"certificates": rep.Certificates, "fleetSealed": rep.FleetSealed,
			"warnings": rep.Warnings,
		})
	case "restore":
		p, ok := backupPath(r.FormValue("name"))
		if !ok {
			http.Error(w, "no such backup in "+app.BackupDir, http.StatusBadRequest)
			return
		}
		f, err := os.Open(p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		res, err := manage.Restore(f)
		if err != nil {
			http.Error(w, "restore failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		s.sessions.clear()
		s.pending.clear() // the archive may have carried another password
		writeJSON(w, map[string]any{"status": "ok", "files": res.Files, "tunnels": res.Tunnels,
			"started": res.Started, "failed": res.Failed, "warnings": res.Warnings})
	case "delete":
		p, ok := backupPath(r.FormValue("name"))
		if !ok {
			http.Error(w, "no such backup in "+app.BackupDir, http.StatusBadRequest)
			return
		}
		if err := os.Remove(p); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"files": backupFiles()})
	case "offsite":
		cmd := strings.TrimSpace(r.FormValue("command"))
		if err := manage.SetOffsiteCommand(cmd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"offsite": manage.OffsiteCommand()})
	case "send":
		// The newest backup through the off-site command, the way the menu
		// offers to try it once it is set.
		newest, err := manage.NewestBackup()
		if err != nil {
			http.Error(w, "no backup yet — create one first", http.StatusBadRequest)
			return
		}
		if err := manage.SendOffsite(newest); err != nil {
			http.Error(w, "it did not work: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"sent": filepath.Base(newest)})
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// handleBackupFile downloads one of the backups kept on this server.
func (s *server) handleBackupFile(w http.ResponseWriter, r *http.Request) {
	p, ok := backupPath(r.URL.Query().Get("name"))
	if !ok {
		http.Error(w, "no such backup", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(p)+`"`)
	w.Header().Set("Cache-Control", "no-store") // it holds every token
	http.ServeFile(w, r, p)
}

// --- update: from a file, and back to a restore point ------------------------

func localUpdateView() map[string]any {
	out := map[string]any{"asset": manage.LocalAssetName(), "searched": manage.LocalUpdateSearchedIn()}
	if u, ok := manage.FindLocalUpdate(); ok {
		out["found"] = map[string]any{"path": u.Path, "size": u.Size, "added": u.When.Unix(),
			"version": u.Version, "verified": u.Checksums != ""}
	}
	return out
}

// runUpkeep runs one long job on the update log the Maintenance screen already
// follows, and restarts the panel last, after the outcome is recorded.
func runUpkeep(job func(logf func(string)) error) bool {
	if running, _, _ := updateProgress.snapshot(); running {
		return false
	}
	updateProgress.start()
	go func() {
		updateProgress.finish(job(updateProgress.log))
		manage.FinishDeferredRestarts()
	}()
	return true
}

func (s *server) handleLocalUpdate(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, localUpdateView())
	case http.MethodPost:
		if r.URL.Query().Get("action") == "install" {
			u, ok := manage.FindLocalUpdate()
			if !ok {
				http.Error(w, "no "+manage.LocalAssetName()+" to install — upload it first", http.StatusBadRequest)
				return
			}
			if !runUpkeep(func(logf func(string)) error { return manage.ApplyLocalUpdate(u, logf) }) {
				http.Error(w, "an update is already running", http.StatusConflict)
				return
			}
			writeJSON(w, map[string]string{"status": "started"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 260<<20)
		if err := r.ParseMultipartForm(16 << 20); err != nil {
			http.Error(w, "the upload was rejected: "+err.Error(), http.StatusBadRequest)
			return
		}
		arch, hdr, err := r.FormFile("archive")
		if err != nil {
			http.Error(w, "no archive was included", http.StatusBadRequest)
			return
		}
		defer arch.Close()
		// An absent SHA256SUMS stays a nil interface, which SaveLocalUpdate
		// reads as "none" and installs unverified, saying so in the log.
		var sums io.Reader
		if f, _, err := r.FormFile("sums"); err == nil {
			defer f.Close()
			sums = f
		}
		if _, err := manage.SaveLocalUpdate(hdr.Filename, arch, sums); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, localUpdateView())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	stamp := r.FormValue("stamp")
	for _, sn := range manage.ListSnapshots() {
		if sn.Meta.Stamp != stamp {
			continue
		}
		if !runUpkeep(func(logf func(string)) error { return manage.RollbackUpdate(sn, logf) }) {
			http.Error(w, "an update is already running", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]string{"status": "started", "version": sn.Meta.Version})
		return
	}
	http.Error(w, "no restore point "+stamp, http.StatusNotFound)
}

// --- the panel itself ----------------------------------------------------------

func (s *server) handlePanelSelf(w http.ResponseWriter, r *http.Request) {
	c := Load()
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"path": c.PathPrefix(), "port": c.Port, "scheme": c.Scheme()})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	// Every one of these ends in a restart of this process, which is also what
	// ends the request. So the change is written, the answer sent, and the
	// restart made a moment later — the same order handlePanelPort uses.
	restart := func() {
		go func() {
			time.Sleep(700 * time.Millisecond)
			manage.RestartService(app.WebUIService)
		}()
	}
	switch r.FormValue("action") {
	case "code":
		s.authMu.Lock()
		defer s.authMu.Unlock()
		var err error
		c, err = UpdateConfig(func(c *Config) error { c.Password = randomDigits(8); return nil })
		if err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		s.sessions.clear()
		s.pending.clear()
		writeJSON(w, map[string]any{"code": c.Password})
		restart()
	case "path":
		next := strings.TrimSpace(r.FormValue("path"))
		switch r.FormValue("mode") {
		case "random":
			next = randomPathSegment()
		case "none":
			next = "/"
		case "custom":
			// next is what was typed, checked below like any other.
		default:
			http.Error(w, "mode must be random, custom or none", http.StatusBadRequest)
			return
		}
		if next == "" || !validBasePath(next) {
			http.Error(w, "a path is one segment of letters, digits, - and _", http.StatusBadRequest)
			return
		}
		if t := strings.Trim(next, "/"); t == "" {
			c.BasePath = "/"
		} else {
			c.BasePath = t
		}
		_, err := UpdateConfig(func(current *Config) error { current.BasePath = c.BasePath; return nil })
		if err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"path": c.PathPrefix()})
		restart()
	case "restart":
		writeJSON(w, map[string]string{"status": "restarting"})
		restart()
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}
