package webui

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/alerthist"
	"github.com/backpack/backpack/internal/app"
)

// The record of what was done through the panel.
//
// "Who restarted the tunnel at three in the morning" had no answer. Nor did
// "when did this token last get used", or "did anybody change this before it
// broke". The journal has the *effect* — a service restarted — and nothing
// about the request that caused it.
//
// The record is written by the authorisation guard rather than by the handlers.
// A log each handler writes for itself is a log with one hole per handler
// somebody forgot to update, and those holes are invisible until the day
// somebody goes looking. Written at the choke point, the only way to perform an
// action without recording it is to perform it without being authorised.
//
// Reads are not recorded. A panel that is being polled every few seconds would
// otherwise bury the handful of lines that matter under thousands that do not,
// and a record nobody can read is not a record.

// auditEntry is one action.
type auditEntry struct {
	At     int64  `json:"at"` // unix seconds
	Who    string `json:"who"`
	IP     string `json:"ip,omitempty"`
	Method string `json:"method"`
	Path   string `json:"path"`
	// Action is the sub-operation for the endpoints that carry one — the panel
	// routes a dozen different fleet operations through /api/nodes, and
	// recording them all as "POST /api/nodes" would be recording nothing.
	Action string `json:"action,omitempty"`
	// Status is what the handler answered, so a refused attempt is
	// distinguishable from one that went through. A failed attempt is often
	// the more interesting line.
	Status int `json:"status,omitempty"`
}

// auditKeep is how many entries are held. Enough to cover the window anyone
// investigates in — weeks of ordinary use — while staying a file that can be
// read and parsed in one go.
//
// A variable so the test that proves the cap holds does not have to write five
// thousand entries to prove it.
var auditKeep = 5000

var (
	auditMu sync.Mutex
	// AuditPath is where the record lives. A variable for the same reason
	// TokensPath is: a test must not append to the real machine's record.
	AuditPath = filepath.Join(app.ConfigDir, "audit.json")
)

// readAudit loads the record. A missing file is an empty one: the panel has to
// work on a machine that has never written it.
func readAudit() []auditEntry {
	data, err := os.ReadFile(AuditPath)
	if err != nil {
		return nil
	}
	var out []auditEntry
	if err := json.Unmarshal(data, &out); err != nil {
		// Reading a damaged record as empty is right — the panel must not stop
		// working because its history did — but doing it silently is not. An
		// audit record that has quietly become empty is indistinguishable from
		// one nobody has written to, and the whole value of the thing is that
		// it can be trusted after the fact.
		log.Printf("audit: %s is damaged and is being read as empty: %v", AuditPath, err)
		return nil
	}
	return out
}

// Audit returns the most recent entries, newest first.
func Audit(limit int) []auditEntry {
	auditMu.Lock()
	defer auditMu.Unlock()
	all := readAudit()
	if limit <= 0 || limit > len(all) {
		limit = len(all)
	}
	out := make([]auditEntry, 0, limit)
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, all[i])
	}
	return out
}

// record appends one entry.
//
// It rewrites the whole file each time, which is the right trade here: writes
// are the handful of actions an operator takes, not the polling, so the cost is
// paid a few times a minute at worst — and the alternative, an append-only file
// with its own rotation, is a second small database to get wrong.
//
// It never returns an error and never blocks the request on a failure: an audit
// file that cannot be written must not be a reason the panel stops working. The
// alternative — refusing the action because it could not be recorded — sounds
// more rigorous and in practice means a full disk locks the operator out of the
// tool they need to fix it.
func record(e auditEntry) {
	// Off the machine first, for the entries that are worth it. Before the
	// local write rather than after: if the disk is what is wrong, the copy
	// somewhere else is the one that still happens.
	forward(e)

	auditMu.Lock()
	defer auditMu.Unlock()

	all := readAudit()
	all = append(all, e)
	if len(all) > auditKeep {
		all = all[len(all)-auditKeep:]
	}
	data, err := json.Marshal(all)
	if err != nil {
		return
	}
	// 0600 and a rename: the record names hosts and operations, and a
	// half-written file would lose every earlier entry rather than the last
	// one.
	tmp := AuditPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, AuditPath)
}

// auditable reports whether a request is worth recording: anything that is not
// a plain read.
func auditable(r *http.Request) bool {
	return r.Method != http.MethodGet && r.Method != http.MethodHead
}

// statusRecorder remembers what the handler answered, so the record can say
// whether the action actually happened.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Flush keeps the streaming endpoints streaming. Wrapping a ResponseWriter
// hides whatever interfaces it also had, and the panel's log tail is one of
// them — without this it buffers until the handler returns, which for a tail is
// for ever.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// auditAction pulls the sub-operation out of a request, for the endpoints that
// multiplex several of them behind one path.
//
// The body has usually not been parsed yet at this point, and parsing it here
// would consume it. ParseForm caches, so a handler that parses afterwards gets
// the same values — but only for a form body, so this deliberately looks at the
// query string and at an already-parsed form, and settles for nothing when the
// body is JSON.
func auditAction(r *http.Request) string {
	if v := r.URL.Query().Get("action"); v != "" {
		return v
	}
	if r.PostForm != nil {
		return r.PostForm.Get("action")
	}
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err == nil {
			return r.PostForm.Get("action")
		}
	}
	return ""
}

// describeAudit renders one entry the way an operator reads it.
func describeAudit(e auditEntry) string {
	what := e.Path
	if e.Action != "" {
		what += " (" + e.Action + ")"
	}
	line := fmt.Sprintf("%s  %s  %s %s",
		time.Unix(e.At, 0).Format("2006-01-02 15:04:05"), e.Who, e.Method, what)
	if e.Status >= 400 {
		line += fmt.Sprintf("  — refused (%d)", e.Status)
	}
	return line
}

// Getting the record off the machine it describes.
//
// The audit file lives at /etc/backpack/audit.json, owned by root, on the box
// the panel runs on — and the panel is root on that box. So an attacker who
// reaches it can rewrite the record of how they got there, and the record's
// whole value is that it can be trusted afterwards.
//
// There is no way to make a local file tamper-proof against local root. What
// there is, is a copy somewhere else, written *as it happens*, so that
// rewriting the local one no longer rewrites the truth.
//
// # What is forwarded, and what is not
//
// Not everything. The panel is polled and most of what it records is somebody
// looking at a page — forwarding that would be thousands of messages a day, and
// a channel nobody reads is a channel that hides the one line that mattered.
//
// So: the actions that change who can do what, the ones that change the fleet,
// and every refusal. Those are the lines somebody would want to alter, which is
// exactly the test for whether they are worth copying.

// worthForwarding reports whether an entry should leave the machine.
func worthForwarding(e auditEntry) bool {
	// Every refusal. A run of these is somebody trying credentials, and it is
	// the earliest signal there is.
	if e.Status >= 400 {
		return true
	}
	switch e.Path {
	case "/api/tokens":
		// Issuing and revoking credentials.
		return true
	case "/api/security", "/api/sessions":
		// The panel's password and its signed-in devices.
		return true
	case "/api/nodes", "/api/node/pair":
		// Adding, removing or upgrading a managed server. A fleet that gains a
		// machine nobody added is the thing this is for.
		switch e.Action {
		case "add", "remove", "credentials", "upgradeall", "pin", "unpin":
			return true
		}
	}
	return false
}

// forward copies one entry to the alert history, which the monitor relays to
// Telegram — off the machine, within seconds, over a channel the attacker on
// this box does not control.
//
// It reuses the alert path rather than adding a second delivery mechanism,
// because a second one is a second thing to configure, to break, and to
// discover was never working.
func forward(e auditEntry) {
	if !worthForwarding(e) {
		return
	}
	what := e.Path
	if e.Action != "" {
		what += " (" + e.Action + ")"
	}
	from := e.IP
	if from == "" {
		from = "an unknown address"
	}
	if e.Status >= 400 {
		alerthist.RecordEvent(fmt.Sprintf(
			"🔒 Panel refused %s %s from %s (%s) — %d",
			e.Method, what, from, e.Who, e.Status))
		return
	}
	alerthist.RecordEvent(fmt.Sprintf(
		"🔑 Panel: %s %s by %s from %s", e.Method, what, e.Who, from))
}
