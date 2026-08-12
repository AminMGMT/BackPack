package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/alerthist"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
)

// The screens that are not about one tunnel: the panel, backups, updates,
// restore points, diagnostics and the record of what has happened.
//
// All of these already existed as CLI menus and panel pages. What they did not
// have was a way in from a phone, which is where the operator is when a tunnel
// drops — the point of the bot is that the answer and the fix are in the same
// place as the notification.

// --- tools ------------------------------------------------------------------

func toolsScreen(lang string) reply {
	text := b("🧰 " + tr(lang, "Tools"))
	if tag, ok := manage.UpdateAvailable(); ok {
		text += "\n\n⬆️ " + fmt.Sprintf(esc(tr(lang, "Version %s is available (you are on %s).")),
			esc(tag), esc(app.Version))
	} else {
		text += "\n\n" + esc(app.Version)
	}

	return screenReply(text, kb(
		row(btn{Text: "🖥 " + tr(lang, "Web Panel"), Data: "nav:webui"},
			btn{Text: "🔐 " + tr(lang, "Backup"), Data: "act:backup"}),
		row(btn{Text: "⬆️ " + tr(lang, "Update"), Data: "nav:update"},
			btn{Text: "♻️ " + tr(lang, "Restore points"), Data: "snap:list"}),
		row(btn{Text: "🛰 " + tr(lang, "Relay check"), Data: "diag:relay"}),
		backTo(lang, "nav:home"),
	))
}

// --- health -----------------------------------------------------------------

// healthScreen runs every diagnostic and leads with the tally, because the
// tally is the answer most of the time and the detail only matters when it is
// not all green.
//
// The sweep dials the public-IP services and every tunnel's port, so it is
// measured in the background like the other slow screens — see measuring.
func healthScreen(lang string) reply {
	r := measuring(lang, "🩺 "+tr(lang, "Health"), "nav:health", "nav:home")
	r.after = func(c Config, chat string, messageID int64) {
		go pushResult(c, chat, messageID, healthResult(lang))
	}
	return r
}

func healthResult(lang string) reply {
	checks := manage.Diagnose()
	ok, warn, fail := manage.CountByLevel(checks)

	var out strings.Builder
	out.WriteString(b("🩺 "+tr(lang, "Health")) + "\n\n")
	fmt.Fprintf(&out, "✅ %d    ⚠️ %d    ❌ %d\n", ok, warn, fail)

	if warn == 0 && fail == 0 {
		out.WriteString("\n" + esc(tr(lang, "Everything checks out.")))
	} else {
		out.WriteString("\n")
		for _, c := range checks {
			var icon string
			switch c.Level {
			case manage.CheckFail:
				icon = "❌"
			case manage.CheckWarn:
				icon = "⚠️"
			default:
				continue // the passing checks are already counted above
			}
			fmt.Fprintf(&out, "%s %s — %s\n", icon, b(c.Group+" · "+c.Name), esc(c.Detail))
			if c.Fix != "" {
				fmt.Fprintf(&out, "   ↳ %s\n", esc(c.Fix))
			}
		}
	}

	return screenReply(out.String(), kb(
		row(btn{Text: "🛰 " + tr(lang, "Relay check"), Data: "diag:relay"}),
		refreshAndBack(lang, "nav:health", "nav:home"),
	))
}

// diagnoseRoute serves the diagnostics that are not the main health sweep.
func diagnoseRoute(lang, rest string) reply {
	if rest == "relay" {
		return relayScreen(lang)
	}
	return healthScreen(lang)
}

// relayScreen walks the chain the bot's own messages travel over.
//
// This is the diagnostic that matters most and the one that is hardest to reach
// when it is needed: if the relay is broken the bot cannot tell you so. It can
// still answer a press, though, which is why the screen is worth having — the
// admin's client reaches Telegram even when this server does not.
//
// Each hop dials with its own timeout, so a broken chain is the slow case as
// well as the interesting one; measured in the background for that reason.
func relayScreen(lang string) reply {
	r := measuring(lang, "🛰 "+tr(lang, "Relay check"), "diag:relay", "nav:tools")
	r.after = func(c Config, chat string, messageID int64) {
		go pushResult(c, chat, messageID, relayResult(lang))
	}
	return r
}

func relayResult(lang string) reply {
	var out strings.Builder
	out.WriteString(b("🛰 "+tr(lang, "Relay check")) + "\n\n")
	out.WriteString(esc(tr(lang, "Route")) + " : " + esc(RelayStatus()) + "\n\n")

	for _, s := range DiagnoseRelay() {
		icon := "❌"
		if s.OK {
			icon = "✅"
		}
		fmt.Fprintf(&out, "%s %s\n", icon, b(s.Name))
		if s.Detail != "" {
			fmt.Fprintf(&out, "   %s\n", esc(s.Detail))
		}
		if s.Fix != "" {
			fmt.Fprintf(&out, "   ↳ %s\n", esc(s.Fix))
		}
	}

	return screenReply(out.String(), kb(refreshAndBack(lang, "diag:relay", "nav:tools")))
}

// --- updating ---------------------------------------------------------------

func updateScreen(c Config, lang string) reply {
	var out strings.Builder
	out.WriteString(b("⬆️ "+tr(lang, "Update")) + "\n\n")
	fmt.Fprintf(&out, tr(lang, "Installed")+" : %s\n", code(app.Version))

	tag, available := manage.UpdateAvailable()
	rows := [][]btn{}
	if available {
		fmt.Fprintf(&out, tr(lang, "Available")+" : %s\n\n", code(tag))
		out.WriteString(esc(tr(lang, "A restore point is saved first, and it rolls itself back if the tunnels do not come back up.")))
		rows = append(rows, row(btn{Text: "⬆️ " + tr(lang, "Update now"), Data: "act:update"}))
	} else {
		out.WriteString("\n" + esc(tr(lang, "This is the newest release.")))
	}
	rows = append(rows, refreshAndBack(lang, "nav:update", "nav:tools"))

	return screenReply(out.String(), kb(rows...))
}

func confirmUpdate(c Config, lang, tok string) reply {
	tag, available := manage.UpdateAvailable()
	if !available {
		r := updateScreen(c, lang)
		r.toast = tr(lang, "This is the newest release.")
		return r
	}
	return confirmScreen(lang,
		fmt.Sprintf(tr(lang, "Update to %s?"), b(tag)),
		esc(tr(lang, "The tunnels restart as part of it. A restore point is saved first.")),
		tr(lang, "Yes, update"),
		"do:update::"+tok, "nav:update")
}

// applyUpdateReply starts the update and streams its progress.
//
// In a goroutine, and not because it is slow to look at: handleUpdate runs
// inside the polling loop, so anything blocking here stops the bot answering
// anything else — including, during an update that goes wrong, the screens that
// would show what went wrong.
func applyUpdateReply(c Config, u tgUser, lang string) reply {
	user := strconv.FormatInt(u.ID, 10)
	if allowed, wait := actionLimiter.allow(user, "update", time.Now()); !allowed {
		r := updateScreen(c, lang)
		r.toast = fmt.Sprintf(tr(lang, "Too quick — try again in %ds."), roundSeconds(wait))
		r.alert = true
		return r
	}

	r := screenReply(b("⬆️ "+tr(lang, "Update"))+"\n\n"+esc(tr(lang, "Updating — this takes a minute.")),
		kb(backTo(lang, "nav:tools")))
	r.toast = tr(lang, "⬆️ Update started")
	r.after = func(c Config, chat string, messageID int64) {
		go func() {
			var log strings.Builder
			err := manage.ApplyUpdate(func(line string) { log.WriteString(line + "\n") })
			recordAudit(AuditEntry{
				Time: time.Now(), User: c.adminLabel(u), Action: "update",
				OK: err == nil, Detail: errText(err),
			})
			head := "✅ " + tr(lang, "Update finished.")
			if err != nil {
				head = "❌ " + tr(lang, "Update failed.") + "\n" + err.Error()
			}
			_ = sendTo(c, chat, b(head)+"\n\n"+preBlock(tailLines(log.String(), 20)),
				kb(backTo(lang, "nav:tools")))
		}()
	}
	return r
}

// --- restore points ---------------------------------------------------------

// snapshotList shows the restore points, newest first.
func snapshotList(c Config, lang string) reply {
	snaps := manage.ListSnapshots()
	var out strings.Builder
	out.WriteString(b("♻️ "+tr(lang, "Restore points")) + "\n\n")

	if len(snaps) == 0 {
		out.WriteString(esc(tr(lang, "None saved yet. One is taken automatically before every update.")))
		return screenReply(out.String(), kb(backTo(lang, "nav:tools")))
	}

	var rows [][]btn
	for i, s := range snaps {
		label := s.Meta.Version
		if label == "" {
			label = s.Meta.Stamp
		}
		fmt.Fprintf(&out, "%d. %s — %s\n", i+1, b(label), esc(snapshotWhen(s)))
		if s.Meta.Reason != "" {
			fmt.Fprintf(&out, "   %s\n", esc(s.Meta.Reason))
		}
		rows = append(rows, row(btn{
			Text: fmt.Sprintf("♻️ %d. %s", i+1, label),
			Data: "act:snap:" + strconv.Itoa(i),
		}))
	}
	out.WriteString("\n" + esc(tr(lang, "Restoring replaces the binary and every config with the saved copy.")))
	rows = append(rows, backTo(lang, "nav:tools"))

	return screenReply(out.String(), kb(rows...))
}

// snapshotWhen renders a snapshot's age, falling back to its stamp when the
// timestamp cannot be parsed — an unreadable date is no reason to hide the
// restore point itself.
func snapshotWhen(s manage.Snapshot) string {
	t, err := time.Parse(time.RFC3339, s.Meta.Created)
	if err != nil {
		return s.Meta.Stamp
	}
	return t.Format("2006-01-02 15:04")
}

func confirmRestore(lang, arg, tok string) reply {
	snaps := manage.ListSnapshots()
	i, err := strconv.Atoi(arg)
	if err != nil || i < 0 || i >= len(snaps) {
		return screenReply(b("♻️ "+tr(lang, "Restore points"))+"\n\n"+
			esc(tr(lang, "That restore point is gone.")), kb(backTo(lang, "nav:tools")))
	}
	s := snaps[i]
	label := s.Meta.Version
	if label == "" {
		label = s.Meta.Stamp
	}
	return confirmScreen(lang,
		fmt.Sprintf(tr(lang, "Roll back to %s?"), b(label)),
		esc(tr(lang, "The binary and every config are replaced with the saved copy, and the tunnels restart.")),
		tr(lang, "Yes, roll back"),
		"do:snap:"+arg+":"+tok, "snap:list")
}

func restoreReply(c Config, u tgUser, lang, arg string) reply {
	snaps := manage.ListSnapshots()
	i, err := strconv.Atoi(arg)
	if err != nil || i < 0 || i >= len(snaps) {
		r := snapshotList(c, lang)
		r.toast = tr(lang, "That restore point is gone.")
		r.alert = true
		return r
	}
	s := snaps[i]

	user := strconv.FormatInt(u.ID, 10)
	if allowed, wait := actionLimiter.allow(user, "restore", time.Now()); !allowed {
		r := snapshotList(c, lang)
		r.toast = fmt.Sprintf(tr(lang, "Too quick — try again in %ds."), roundSeconds(wait))
		r.alert = true
		return r
	}

	r := screenReply(b("♻️ "+tr(lang, "Restore points"))+"\n\n"+esc(tr(lang, "Rolling back — this takes a minute.")),
		kb(backTo(lang, "nav:tools")))
	r.toast = tr(lang, "♻️ Rollback started")
	r.after = func(c Config, chat string, messageID int64) {
		go func() {
			var log strings.Builder
			err := manage.RestoreSnapshot(s, func(line string) { log.WriteString(line + "\n") })
			recordAudit(AuditEntry{
				Time: time.Now(), User: c.adminLabel(u), Action: "restore",
				Target: s.Meta.Stamp, OK: err == nil, Detail: errText(err),
			})
			head := "✅ " + tr(lang, "Rollback finished.")
			if err != nil {
				head = "❌ " + tr(lang, "Rollback failed.") + "\n" + err.Error()
			}
			_ = sendTo(c, chat, b(head)+"\n\n"+preBlock(tailLines(log.String(), 20)),
				kb(backTo(lang, "nav:tools")))
		}()
	}
	return r
}

// --- history ----------------------------------------------------------------

// historyEntries is how many rows each history screen shows. Enough to cover
// last night; short enough to read without scrolling.
const historyEntries = 12

// historyScreen shows what the watcher has reported recently, and what is wrong
// right now — the two questions "was I asleep when it broke" needs.
func historyScreen(c Config, lang string) reply {
	st := alerthist.Load()
	var out strings.Builder
	out.WriteString(b("🕘 "+tr(lang, "Alert history")) + "\n\n")

	if len(st.Active) > 0 {
		out.WriteString(b(tr(lang, "Firing now")) + "\n")
		for _, a := range st.Active {
			out.WriteString("• " + esc(a) + "\n")
		}
		out.WriteString("\n")
	}

	if len(st.Events) == 0 {
		out.WriteString(esc(tr(lang, "Nothing has been reported yet.")))
	} else {
		events := st.Events
		if len(events) > historyEntries {
			events = events[len(events)-historyEntries:]
		}
		// Newest first: the reason anyone opens this screen is the most recent
		// thing that happened.
		for i := len(events) - 1; i >= 0; i-- {
			e := events[i]
			fmt.Fprintf(&out, "%s  %s\n", code(e.Time.Format("01-02 15:04")),
				esc(firstLine(e.Message)))
		}
	}

	return screenReply(out.String(), kb(
		row(btn{Text: "📋 " + tr(lang, "Bot actions"), Data: "nav:audit"}),
		refreshAndBack(lang, "nav:history", "nav:home"),
	))
}

// auditScreen shows who drove the bot, which with more than one admin is the
// only record of why a tunnel restarted.
func auditScreen(c Config, lang string) reply {
	entries := LoadAudit()
	var out strings.Builder
	out.WriteString(b("📋 "+tr(lang, "Bot actions")) + "\n\n")

	if len(entries) == 0 {
		out.WriteString(esc(tr(lang, "No actions have been taken through the bot yet.")))
	} else {
		if len(entries) > historyEntries {
			entries = entries[len(entries)-historyEntries:]
		}
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			icon := "✅"
			if !e.OK {
				icon = "❌"
			}
			line := e.Action
			if e.Target != "" {
				line += " " + e.Target
			}
			fmt.Fprintf(&out, "%s %s  %s — %s\n", icon,
				code(e.Time.Format("01-02 15:04")), esc(line), esc(e.User))
		}
	}

	return screenReply(out.String(), kb(
		row(btn{Text: "🔔 " + tr(lang, "Alert history"), Data: "nav:history"}),
		refreshAndBack(lang, "nav:audit", "nav:home"),
	))
}

// --- helpers ----------------------------------------------------------------

// firstLine trims a multi-line alert to its headline, which is the part that
// identifies it; the rest is context that does not belong in a list.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// tailLines keeps the last n lines of output — for a log, the end is the part
// that says how it turned out.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
