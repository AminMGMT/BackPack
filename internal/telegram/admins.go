package telegram

import (
	"strconv"
	"strings"
)

// Who is allowed to talk to the bot.
//
// There used to be exactly one answer: AdminID, the account that configured it.
// That is still the owner and still the only account that can never be locked
// out — but a tunnel is rarely run by one person alone, and the alternative to
// a second admin was sharing the owner's Telegram account.
//
// A second account is not automatically a second owner. Most people who need to
// see whether a tunnel is up have no business restarting it, so an admin can be
// marked read-only: every screen, no actions. The distinction is enforced in
// one place, canWrite, and every action asks it.

// Admin is one account allowed to use the bot.
type Admin struct {
	ID string `json:"id"`
	// ReadOnly withholds every action: the account can look at anything and
	// change nothing.
	ReadOnly bool `json:"read_only,omitempty"`
	// Label is a human name for the audit log and the admin list. Optional.
	Label string `json:"label,omitempty"`
}

// isAdmin reports whether an account may use the bot at all.
func (c Config) isAdmin(id string) bool {
	if id == "" {
		return false
	}
	if id == c.AdminID {
		return true
	}
	for _, a := range c.Admins {
		if a.ID == id {
			return true
		}
	}
	return false
}

// canWrite reports whether an account may run actions that change something.
//
// The owner always can. Making the owner revocable would mean a mis-saved
// config could leave a server with a bot nobody is allowed to drive.
func (c Config) canWrite(id string) bool {
	if id == "" {
		return false
	}
	if id == c.AdminID {
		return true
	}
	for _, a := range c.Admins {
		if a.ID == id {
			return !a.ReadOnly
		}
	}
	return false
}

// recipients lists every chat that should receive unprompted messages, owner
// first and without duplicates.
func (c Config) recipients() []string {
	if c.AdminID == "" {
		return nil
	}
	out := []string{c.AdminID}
	seen := map[string]bool{c.AdminID: true}
	for _, a := range c.Admins {
		if a.ID == "" || seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		out = append(out, a.ID)
	}
	return out
}

// adminLabel names an account for the audit log: its label if it has one, its
// Telegram username if not, and the bare id as a last resort — which is still
// enough to tell two people apart.
func (c Config) adminLabel(u tgUser) string {
	id := strconv.FormatInt(u.ID, 10)
	for _, a := range c.Admins {
		if a.ID == id && a.Label != "" {
			return a.Label
		}
	}
	switch {
	case u.Username != "":
		return "@" + u.Username
	case u.FirstName != "":
		return u.FirstName
	case id == c.AdminID:
		return "owner"
	}
	return id
}

// ParseAdmins reads a comma or newline separated admin list, where an id may
// carry a ":ro" suffix to mark it read-only. It is what the CLI and the web
// panel hand the user, because asking for JSON would be worse.
func ParseAdmins(s string) []Admin {
	var out []Admin
	seen := map[string]bool{}
	for _, field := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';' || r == ' '
	}) {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		a := Admin{ID: field}
		if id, suffix, ok := strings.Cut(field, ":"); ok {
			a.ID = strings.TrimSpace(id)
			a.ReadOnly = strings.EqualFold(strings.TrimSpace(suffix), "ro")
		}
		if _, err := strconv.ParseInt(a.ID, 10, 64); err != nil {
			continue // not a Telegram id; silently ignored rather than saved broken
		}
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		out = append(out, a)
	}
	return out
}

// AdminCount is how many accounts may use the bot, the owner included.
func AdminCount(c Config) int { return len(c.recipients()) }

// AdminsSummary renders the admin list for the CLI and the panel.
func AdminsSummary(c Config) string {
	var out strings.Builder
	out.WriteString(c.AdminID + " (owner)")
	for _, a := range c.Admins {
		if a.ID == c.AdminID {
			continue
		}
		out.WriteString("\n" + a.ID)
		if a.Label != "" {
			out.WriteString(" — " + a.Label)
		}
		if a.ReadOnly {
			out.WriteString(" (read-only)")
		}
	}
	return out.String()
}
