package telegram

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// A record of every action taken through the bot.
//
// The bot can now stop a tunnel, apply an update and roll the machine back to a
// restore point. With more than one admin, "why did this restart at 3am" stops
// being a question anyone can answer from memory — and the systemd journal
// records that the service restarted, not who asked for it.
//
// Kept deliberately small: the last hundred actions, in one file, next to the
// alert history that works the same way.

// maxAudit bounds the file.
const maxAudit = 100

// AuditEntry is one action.
type AuditEntry struct {
	Time   time.Time `json:"time"`
	User   string    `json:"user"`
	Action string    `json:"action"`
	Target string    `json:"target,omitempty"`
	// OK is whether it worked; Detail carries the error when it did not.
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

var auditMu sync.Mutex

func auditPath() string { return app.ConfigDir + "/telegram-audit.json" }

// recordAudit appends one entry, trimming the file to the newest maxAudit.
//
// Failures to write are ignored on purpose. An audit log that could block a
// restart because the disk is full would turn a logging problem into an outage,
// and the disk being full is exactly when someone is restarting things.
func recordAudit(e AuditEntry) {
	auditMu.Lock()
	defer auditMu.Unlock()

	entries := loadAuditLocked()
	entries = append(entries, e)
	if len(entries) > maxAudit {
		entries = entries[len(entries)-maxAudit:]
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return
	}
	_ = app.WriteFileAtomic(auditPath(), data, 0600)
}

func loadAuditLocked() []AuditEntry {
	data, err := os.ReadFile(auditPath())
	if err != nil {
		return nil
	}
	var out []AuditEntry
	if json.Unmarshal(data, &out) != nil {
		return nil
	}
	return out
}

// LoadAudit returns the recorded actions, oldest first.
func LoadAudit() []AuditEntry {
	auditMu.Lock()
	defer auditMu.Unlock()
	return loadAuditLocked()
}
