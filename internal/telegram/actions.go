package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/manage"
)

// Everything that changes something.
//
// One entry point, because the three guards around an action are easy to add
// and easy to forget: the account must be allowed to write, the rate limit must
// let it through, and anything irreversible must have been confirmed within the
// last couple of minutes. Routing every action through actionReply means a new
// button cannot be wired up in a way that skips one of them.
//
// Actions come in two halves. "act:" proposes — it either runs something safe
// straight away or draws the confirmation screen. "do:" performs, and is
// reachable only from a confirmation that carries a fresh token.

// actionReply is the single gate every state-changing press passes through.
func actionReply(c Config, u tgUser, lang, verb, rest string) reply {
	user := strconv.FormatInt(u.ID, 10)
	if !c.canWrite(user) {
		r := navigate(c, lang, "home")
		r.toast = tr(lang, "Your access is read-only.")
		r.alert = true
		return r
	}
	if verb == "act" {
		return propose(c, u, lang, rest)
	}
	return perform(c, u, lang, rest)
}

// propose runs the harmless actions and asks about the rest.
//
// Start is not on the confirmation list on purpose. Starting a tunnel that is
// already running does nothing, and starting one that is down is the thing the
// operator opened the bot to do — putting a dialog in front of it would train
// them to tap through dialogs.
func propose(c Config, u tgUser, lang, rest string) reply {
	action, arg, _ := strings.Cut(rest, ":")
	tok := stamp()

	switch action {
	case "backup":
		return backupReply(c, u, lang)

	case "start":
		return runTunnelAction(c, u, lang, "start", arg)

	case "stop", "restart":
		t, ok := tunnelByID(arg)
		if !ok {
			return goneReply(lang)
		}
		question := fmt.Sprintf(tr(lang, "Stop tunnel %s?"), b(t.Name))
		label := tr(lang, "Yes, stop it")
		detail := tr(lang, "Everything it carries drops until it is started again.")
		if action == "restart" {
			question = fmt.Sprintf(tr(lang, "Restart tunnel %s?"), b(t.Name))
			label = tr(lang, "Yes, restart it")
			detail = tr(lang, "Connections through it drop and reconnect.")
		}
		return confirmScreen(lang, question, esc(detail), label,
			"do:"+action+":"+arg+":"+tok, "t:"+arg)

	case "restartall":
		n := len(manage.List())
		return confirmScreen(lang,
			fmt.Sprintf(tr(lang, "Restart all %d tunnels?"), n),
			esc(tr(lang, "Every connection drops and reconnects.")),
			tr(lang, "Yes, restart all"),
			"do:restartall::"+tok, "nav:tunnels")

	case "update":
		return confirmUpdate(c, lang, tok)

	case "snap":
		return confirmRestore(lang, arg, tok)
	}

	return navigate(c, lang, "home")
}

// perform executes a confirmed action.
func perform(c Config, u tgUser, lang, rest string) reply {
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) != 3 {
		return navigate(c, lang, "home")
	}
	action, arg, token := parts[0], parts[1], parts[2]

	if !confirmFresh(token, time.Now()) {
		r := navigate(c, lang, "home")
		r.toast = tr(lang, "That confirmation has expired — please try again.")
		r.alert = true
		return r
	}

	switch action {
	case "stop", "restart":
		return runTunnelAction(c, u, lang, action, arg)
	case "restartall":
		return restartAllReply(c, u, lang)
	case "update":
		return applyUpdateReply(c, u, lang)
	case "snap":
		return restoreReply(c, u, lang, arg)
	}
	return navigate(c, lang, "home")
}

// --- tunnel start / stop / restart ------------------------------------------

// settleDelay is how long to let systemd act before reading the state back.
// Without it the screen redraws from the state the tunnel had a moment ago and
// shows the button as having done nothing.
const settleDelay = 1200 * time.Millisecond

// runTunnelAction drives one tunnel and answers with its refreshed screen.
func runTunnelAction(c Config, u tgUser, lang, action, id string) reply {
	t, ok := tunnelByID(id)
	if !ok {
		return goneReply(lang)
	}

	user := strconv.FormatInt(u.ID, 10)
	if allowed, wait := actionLimiter.allow(user, action+":"+id, time.Now()); !allowed {
		r := tunnelScreen(lang, t)
		r.toast = fmt.Sprintf(tr(lang, "Too quick — try again in %ds."), roundSeconds(wait))
		r.alert = true
		return r
	}

	var err error
	switch action {
	case "start":
		err = manage.Start(t.Name)
	case "stop":
		err = manage.Stop(t.Name)
	case "restart":
		err = manage.Restart(t.Name)
	}

	recordAudit(AuditEntry{
		Time: time.Now(), User: c.adminLabel(u), Action: action, Target: t.Name,
		OK: err == nil, Detail: errText(err),
	})

	if err != nil {
		r := tunnelScreen(lang, t)
		r.toast = "⚠️ " + err.Error()
		r.alert = true
		return r
	}

	// Starting and restarting have something to wait for; stopping does not.
	if action == "stop" {
		time.Sleep(settleDelay)
	} else {
		manage.WaitServiceActive(t.Service, 6*time.Second)
	}

	r := tunnelScreen(lang, t)
	switch action {
	case "start":
		r.toast = fmt.Sprintf(tr(lang, "▶️ %s started"), t.Name)
	case "stop":
		r.toast = fmt.Sprintf(tr(lang, "⏹ %s stopped"), t.Name)
	case "restart":
		r.toast = fmt.Sprintf(tr(lang, "🔄 %s restarted"), t.Name)
	}
	return r
}

// restartAllReply restarts everything and reports the tally.
func restartAllReply(c Config, u tgUser, lang string) reply {
	user := strconv.FormatInt(u.ID, 10)
	if allowed, wait := actionLimiter.allow(user, "restartall", time.Now()); !allowed {
		r := tunnelsScreen(lang)
		r.toast = fmt.Sprintf(tr(lang, "Too quick — try again in %ds."), roundSeconds(wait))
		r.alert = true
		return r
	}

	ok, failed := manage.RestartAll()
	recordAudit(AuditEntry{
		Time: time.Now(), User: c.adminLabel(u), Action: "restart-all",
		OK: failed == 0, Detail: fmt.Sprintf("%d ok, %d failed", ok, failed),
	})
	time.Sleep(settleDelay)

	r := tunnelsScreen(lang)
	if failed == 0 {
		r.toast = fmt.Sprintf(tr(lang, "🔄 %d tunnels restarted"), ok)
	} else {
		r.toast = fmt.Sprintf(tr(lang, "🔄 %d restarted, %d failed"), ok, failed)
		r.alert = true
	}
	return r
}

// errText renders an error for the audit log without a nil check at every call.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// --- backup -----------------------------------------------------------------

// backupReply acknowledges immediately and uploads afterwards. Building the
// archive takes long enough that a silent pause reads as a dead button.
func backupReply(c Config, u tgUser, lang string) reply {
	user := strconv.FormatInt(u.ID, 10)
	if allowed, wait := actionLimiter.allow(user, "backup", time.Now()); !allowed {
		r := toolsScreen(lang)
		r.toast = fmt.Sprintf(tr(lang, "Too quick — try again in %ds."), roundSeconds(wait))
		r.alert = true
		return r
	}

	r := toolsScreen(lang)
	r.toast = tr(lang, "🔐 Preparing your backup…")
	r.after = func(c Config, chat string, messageID int64) {
		err := sendBackup(c)
		recordAudit(AuditEntry{
			Time: time.Now(), User: c.adminLabel(u), Action: "backup",
			OK: err == nil, Detail: errText(err),
		})
		if err != nil {
			_ = sendTo(c, chat, "⚠️ "+esc(tr(lang, "Backup failed"))+"\n"+code(err.Error()),
				kb(backTo(lang, "nav:tools")))
		}
	}
	return r
}
