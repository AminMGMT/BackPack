package telegram

import (
	"encoding/json"
	"strings"
	"time"
)

// The screen layer.
//
// Every button press and every command resolves to exactly one reply: a body, a
// keyboard, and optionally a toast. Nothing in this package sends a message
// directly any more, which is what makes two properties hold without anyone
// having to remember them — a screen reached by button and the same screen
// reached by command are the same screen, and navigation edits the message in
// place instead of appending to the chat.

// btn is one inline button.
type btn struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	URL  string `json:"url,omitempty"`
}

// row is a horizontal group of buttons. Three is the most that stays readable
// on a phone once a status dot and a name are in the label.
func row(bs ...btn) []btn { return bs }

// kb marshals rows into a reply_markup. Marshalling rather than string
// concatenation because tunnel names reach the labels, and a name with a quote
// in it would otherwise produce a keyboard Telegram silently drops.
func kb(rows ...[]btn) string {
	rows = compact(rows)
	if len(rows) == 0 {
		return ""
	}
	data, err := json.Marshal(map[string][][]btn{"inline_keyboard": rows})
	if err != nil {
		return ""
	}
	return string(data)
}

// compact drops empty rows, so a caller can build a row conditionally and pass
// nil when it does not apply.
func compact(rows [][]btn) [][]btn {
	out := rows[:0]
	for _, r := range rows {
		if len(r) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// reply is what one press produces.
type reply struct {
	text     string
	keyboard string
	// edit replaces the message the button was on. False for anything that
	// should stand as its own entry in the chat — a backup file, a log dump.
	edit bool
	// toast appears briefly over the chat without adding a message; alert makes
	// it a dialog that has to be dismissed.
	toast string
	alert bool
	// after runs once the screen has been delivered, for work too slow to hold
	// the poll loop open: it is handed the message it may replace when the
	// answer arrives.
	after func(c Config, chat string, messageID int64)
}

// pushResult replaces a placeholder screen with the finished one, or sends it
// as a new message when there is nothing on screen to replace.
func pushResult(c Config, chat string, messageID int64, r reply) {
	if messageID != 0 {
		if err := editMessage(c, chat, messageID, r.text, r.keyboard); err == nil {
			return
		}
	}
	_ = sendTo(c, chat, r.text, r.keyboard)
}

// measuring builds the placeholder a slow screen shows while it works.
//
// Every screen that dials something needs this. handleUpdate runs inside the
// polling loop, so a screen that takes ten seconds to build is ten seconds in
// which the bot answers nothing else — and Telegram stops accepting the answer
// to a button press long before a twelve-probe path measurement is done. So the
// slow screens draw immediately, measure in the background, and replace
// themselves.
func measuring(lang, title, self, back string) reply {
	return screenReply(
		b(title)+"\n\n"+esc(tr(lang, "Measuring…")),
		kb(refreshAndBack(lang, self, back)))
}

// screenReply is the common case: a body, a keyboard, delivered by editing.
func screenReply(text, keyboard string) reply {
	return reply{text: text, keyboard: keyboard, edit: true}
}

// deliver puts a reply on screen.
//
// An edit that fails falls back to sending. Telegram refuses to edit a message
// older than 48 hours, and a bot that answered nothing at all in that case
// would look hung — a new message is a worse outcome than an edit, but a far
// better one than silence.
func (r reply) deliver(c Config, chat string, messageID int64) {
	if r.text != "" {
		if r.edit && messageID != 0 {
			if err := editMessage(c, chat, messageID, r.text, r.keyboard); err != nil {
				_ = sendTo(c, chat, r.text, r.keyboard)
			}
		} else {
			_ = sendTo(c, chat, r.text, r.keyboard)
		}
	}
	if r.after != nil {
		r.after(c, chat, messageID)
	}
}

// --- the home screen --------------------------------------------------------

// homeKeyboard is the menu every unprompted message carries, so an alert
// arriving at 3am is also a way into the bot rather than just a notification.
func homeKeyboard(lang string) string {
	return kb(
		row(btn{Text: "📊 " + tr(lang, "Overview"), Data: "nav:overview"},
			btn{Text: "🎛 " + tr(lang, "Tunnels"), Data: "nav:tunnels"}),
		row(btn{Text: "🖥 " + tr(lang, "System"), Data: "nav:system"},
			btn{Text: "🔔 " + tr(lang, "Alerts"), Data: "nav:alerts"}),
		row(btn{Text: "🩺 " + tr(lang, "Health"), Data: "nav:health"},
			btn{Text: "🕘 " + tr(lang, "History"), Data: "nav:history"}),
		row(btn{Text: "🧰 " + tr(lang, "Tools"), Data: "nav:tools"},
			btn{Text: "💛 " + tr(lang, "Support"), Data: "nav:support"}),
	)
}

// backTo builds the row that every sub-screen ends with.
func backTo(lang, data string) []btn {
	return row(btn{Text: "⬅️ " + tr(lang, "Back"), Data: data})
}

// refreshAndBack is the footer of any screen showing a live reading.
func refreshAndBack(lang, refresh, back string) []btn {
	return row(
		btn{Text: "🔃 " + tr(lang, "Refresh"), Data: refresh},
		btn{Text: "⬅️ " + tr(lang, "Back"), Data: back},
	)
}

// --- routing ----------------------------------------------------------------

// route turns callback data into a reply.
//
// Read-only screens are answered for anyone on the admin list. Anything that
// changes state passes through actionReply, which is the single point where
// write permission, the rate limit and the confirmation are checked — so a new
// action cannot be added that quietly skips one of them.
func route(c Config, u tgUser, data string) reply {
	lang := c.Language()
	verb, rest, _ := strings.Cut(data, ":")

	switch verb {
	case "nav":
		return navigate(c, lang, rest)
	case "t":
		return tunnelRoute(lang, rest)
	case "act", "do":
		return actionReply(c, u, lang, verb, rest)
	case "alert":
		return alertToggle(c, u, lang, rest)
	case "snap":
		return snapshotList(c, lang)
	case "diag":
		return diagnoseRoute(lang, rest)
	case "noop":
		return reply{}
	}
	return navigate(c, lang, "home")
}

// navScreens names every read-only screen. It is what the tests check buttons
// and commands against, in both directions: a button pointing at a screen not
// listed here is a typo, and a screen listed here that nothing links to is code
// no user can reach.
var navScreens = []string{
	"home", "overview", "tunnels", "system", "alerts",
	"health", "history", "audit", "tools", "update", "webui", "support",
}

// navigate serves the read-only screens.
func navigate(c Config, lang, name string) reply {
	switch name {
	case "overview":
		return screenReply(overviewText(lang), overviewKeyboard(lang))
	case "tunnels":
		return tunnelsScreen(lang)
	case "system":
		return screenReply(SystemText(), refreshBack(lang, "nav:system"))
	case "alerts":
		return alertsScreen(c, lang)
	case "health":
		return healthScreen(lang)
	case "history":
		return historyScreen(c, lang)
	case "audit":
		return auditScreen(c, lang)
	case "tools":
		return toolsScreen(lang)
	case "update":
		return updateScreen(c, lang)
	case "webui":
		return screenReply(webUIText(lang), refreshBack(lang, "nav:webui"))
	case "support":
		return screenReply(supportText(lang), kb(backTo(lang, "nav:home")))
	}
	return screenReply(helpText(lang), homeKeyboard(lang))
}

// refreshBack is the two-button footer used by the simple screens, wrapped as a
// whole keyboard.
func refreshBack(lang, self string) string {
	return kb(refreshAndBack(lang, self, "nav:home"))
}

// overviewText is the full status report — the screen the bot leads with.
func overviewText(lang string) string {
	return b("📊 "+tr(lang, "Overview")) + "\n\n" + StatusText()
}

func overviewKeyboard(lang string) string {
	return kb(
		row(btn{Text: "🎛 " + tr(lang, "Manage tunnels"), Data: "nav:tunnels"}),
		refreshAndBack(lang, "nav:overview", "nav:home"),
	)
}

// --- shared confirmation ----------------------------------------------------

// confirmScreen asks before something irreversible, spelling out what will
// happen rather than asking "are you sure?" — the phrasing that gets a reflexive
// yes.
func confirmScreen(lang, question, detail, confirmLabel, confirmData, cancelData string) reply {
	text := b("⚠️ "+tr(lang, "Confirm")) + "\n\n" + question
	if detail != "" {
		text += "\n\n" + detail
	}
	return screenReply(text, kb(
		row(btn{Text: "✅ " + confirmLabel, Data: confirmData},
			btn{Text: "❌ " + tr(lang, "Cancel"), Data: cancelData}),
	))
}

// stamp is the confirmation token for a screen being offered now.
func stamp() string { return confirmToken(time.Now()) }
