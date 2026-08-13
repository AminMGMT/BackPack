package telegram

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The Telegram Bot API calls, in one place.
//
// Two things here are not obvious and both were bugs before this file existed.
//
// First, parse mode. The alert messages have always written a tunnel name as
// *name*, but no request ever set parse_mode, so every alert arrived with the
// asterisks in it. Everything now goes out as HTML, which means everything that
// could contain a character HTML cares about has to be escaped — see esc.
//
// Second, editing. A bot whose every button press appends a message turns the
// chat into a transcript you have to scroll past to find the current state.
// Navigation edits the message in place instead, so the menu behaves like a
// screen rather than a log.

const parseMode = "HTML"

// esc escapes text for HTML parse mode. Tunnel names, log output, peer
// descriptions and error strings all reach the message body, and any of them
// may contain a character that would otherwise be read as a tag — at which
// point Telegram rejects the whole message and the bot goes silent.
func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// b wraps text in bold, escaping it first.
func b(s string) string { return "<b>" + esc(s) + "</b>" }

// code wraps text in a monospace span, which on a phone is also the thing you
// can tap to copy — worth doing for anything the operator has to paste
// somewhere: ports, addresses, passwords.
func code(s string) string { return "<code>" + esc(s) + "</code>" }

// apiResult is the envelope every Bot API method answers with.
type apiResult struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

// call posts one Bot API method and reports what Telegram said about it.
//
// The description matters as much as the error: "message is not modified" is a
// 400 that means the edit succeeded in every sense the caller cares about, and
// telling it apart from a real failure needs the body, not the status code.
func call(c Config, method string, form url.Values) (apiResult, error) {
	client, err := botClient(c, 20*time.Second)
	if err != nil {
		return apiResult{}, err
	}
	resp, err := client.PostForm(fmt.Sprintf("https://api.telegram.org/bot%s/%s", c.Token, method), form)
	if err != nil {
		// The URL carries the bot token, and Go puts the whole URL in a transport
		// error — so an unedited error hands the credential to whoever reads the
		// screen or the journal.
		return apiResult{}, fmt.Errorf("%s", redactToken(err.Error(), c.Token))
	}
	defer resp.Body.Close()

	var out apiResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		if resp.StatusCode != http.StatusOK {
			// The body was not JSON, so there is no description to quote; the
			// status still gets the treatment it deserves. 401 in particular must
			// not read as a network fault.
			if resp.StatusCode == http.StatusUnauthorized {
				return out, fmt.Errorf("telegram rejected the bot token (401 Unauthorized) — " +
					"the relay is fine; get a fresh token from @BotFather and set it with: " +
					"sudo backpack → Telegram Bot → Configure")
			}
			return out, fmt.Errorf("telegram API returned status %d", resp.StatusCode)
		}
		return out, err
	}
	if !out.OK {
		if resp.StatusCode == http.StatusUnauthorized {
			return out, fmt.Errorf("telegram rejected the bot token (401 %s) — "+
				"the relay is fine; get a fresh token from @BotFather and set it with: "+
				"sudo backpack → Telegram Bot → Configure", strings.TrimSpace(out.Description))
		}
		return out, fmt.Errorf("telegram: %s", out.Description)
	}
	return out, nil
}

// postMessage posts a message via the Telegram Bot API, optionally with an
// inline keyboard (reply_markup).
func postMessage(client *http.Client, botToken, chatID, text, replyMarkup string) error {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	resp, err := client.PostForm(endpoint, messageForm(chatID, text, replyMarkup))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return describeStatus(resp)
	}
	return nil
}

// describeStatus turns a failed Bot API response into an error that says what
// to do about it.
//
// A bare "telegram API returned status 401" sent an operator looking at their
// tunnel, which is the one thing that cannot cause it: the relay had already
// carried the request to Telegram and Telegram had answered. 401 from the Bot
// API means one thing only — the token is not accepted — and saying so turns an
// afternoon of relay debugging into a visit to @BotFather. Telegram also puts a
// plain description in the body of every refusal, which is more specific than
// any status code, so it is read out when present.
func describeStatus(resp *http.Response) error {
	var res apiResult
	if body, err := io.ReadAll(io.LimitReader(resp.Body, 4096)); err == nil {
		_ = json.Unmarshal(body, &res)
	}
	detail := strings.TrimSpace(res.Description)

	if resp.StatusCode == http.StatusUnauthorized {
		if detail == "" {
			detail = "Unauthorized"
		}
		return fmt.Errorf("telegram rejected the bot token (401 %s) — the relay is fine; "+
			"get a fresh token from @BotFather and set it with: sudo backpack → Telegram Bot → Configure", detail)
	}
	if detail != "" {
		return fmt.Errorf("telegram API returned status %d: %s", resp.StatusCode, detail)
	}
	return fmt.Errorf("telegram API returned status %d", resp.StatusCode)
}

// messageForm builds the shared body of sendMessage and editMessageText.
//
// Link previews are off throughout. The support screen and the panel screen are
// mostly URLs, and a preview card under each one pushes the buttons off the
// screen on a phone.
func messageForm(chatID, text, replyMarkup string) url.Values {
	form := url.Values{}
	form.Set("chat_id", chatID)
	form.Set("text", clampText(text))
	form.Set("parse_mode", parseMode)
	form.Set("disable_web_page_preview", "true")
	if replyMarkup != "" {
		form.Set("reply_markup", replyMarkup)
	}
	return form
}

// maxMessage is Telegram's limit on a message body. Logs are the screen that
// reaches it, and a rejected message is a button that looks broken.
const maxMessage = 4000

// preBudget is how much raw text a monospace block may carry. Escaping can
// treble a character ("&" becomes "&amp;"), and the screen has a title and a
// keyboard to fit as well, so the budget is well under the message limit — the
// point is that clampText below never has to run.
const preBudget = 2400

// preBlock renders output as a monospace block sized to fit in a message.
//
// The end is what is kept. Every caller is showing the tail of something — a
// journal, an update log — and the last lines are the ones that say how it
// turned out.
func preBlock(s string) string {
	s = strings.TrimRight(s, "\n")
	if len(s) > preBudget {
		s = s[len(s)-preBudget:]
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = "…\n" + strings.ToValidUTF8(s, "")
	}
	return "<pre>" + esc(s) + "</pre>"
}

// clampText is the last line of defence against an over-long body.
//
// It should never fire — preBlock bounds the only unbounded content — so it is
// written to fail safely rather than well. Cutting a body mid-tag would make
// Telegram reject the whole message, and a message that does not arrive is far
// worse than one that arrives without its bold: so the trimmed form is stripped
// back to plain text before it is cut.
func clampText(s string) string {
	if len(s) <= maxMessage {
		return s
	}
	plain := stripTags(s)
	if len(plain) > maxMessage-8 {
		plain = plain[len(plain)-(maxMessage-8):]
		if i := strings.IndexByte(plain, '\n'); i >= 0 {
			plain = plain[i+1:]
		}
	}
	return "…\n" + strings.ToValidUTF8(plain, "")
}

// stripTags removes HTML markup and turns the escapes back into the characters
// they stand for, yielding something safe to send with no parse mode at all.
func stripTags(s string) string {
	var out strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			out.WriteRune(r)
		}
	}
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(out.String())
}

// sendTo delivers one message with the given keyboard.
func sendTo(c Config, chatID, text, keyboard string) error {
	client, err := botClient(c, 20*time.Second)
	if err != nil {
		return err
	}
	return postMessage(client, c.Token, chatID, text, keyboard)
}

// send delivers a message (with the home menu attached) to the chat.
func send(c Config, chatID, text string) error {
	return sendTo(c, chatID, text, homeKeyboard(c.Language()))
}

// sendPlain delivers text that was not written as HTML.
//
// The web panel hands the bot login codes and sign-in notices as plain strings.
// Everything now goes out with parse_mode set, so an unescaped "<" in one of
// them would have Telegram reject the message — and the message it would reject
// is the one carrying the code needed to log in.
func sendPlain(c Config, chatID, text string) error {
	return sendTo(c, chatID, esc(text), homeKeyboard(c.Language()))
}

// broadcast delivers one message to every admin.
//
// Alerts used to go to the owner alone, which made a second admin an account
// that could press buttons but would never be told a tunnel had dropped — the
// half of the bot that actually matters.
func broadcast(c Config, text string) {
	for _, id := range c.recipients() {
		_ = sendTo(c, id, text, homeKeyboard(c.Language()))
	}
}

// editMessage replaces the text and keyboard of a message already in the chat.
//
// "message is not modified" is reported as success. It happens whenever Refresh
// is pressed and nothing has changed, which is not an error and must not
// surface as one — the alternative is a bot that says "failed" for a screen
// that is simply already correct.
func editMessage(c Config, chatID string, messageID int64, text, keyboard string) error {
	form := messageForm(chatID, text, keyboard)
	form.Set("message_id", strconv.FormatInt(messageID, 10))

	_, err := call(c, "editMessageText", form)
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	return err
}

// answerCallback closes the loading spinner on a pressed button, optionally
// with a message.
//
// A bare acknowledgement leaves an action like Restart with no visible result
// until the screen redraws. The text lands as a toast over the chat: immediate,
// and gone by itself, which suits "done" far better than another message would.
// alert turns it into a dialog the user has to dismiss, for the few results
// worth interrupting for.
func answerCallback(c Config, id, text string, alert bool) {
	form := url.Values{}
	form.Set("callback_query_id", id)
	if text != "" {
		// The toast is capped by Telegram at 200 characters.
		if len(text) > 200 {
			text = text[:200]
		}
		form.Set("text", text)
	}
	if alert {
		form.Set("show_alert", "true")
	}
	_, _ = call(c, "answerCallbackQuery", form)
}

// setMyCommands registers the command list Telegram offers when the user types
// a slash. Without it the bot understands commands nobody can discover.
func setMyCommands(c Config) {
	lang := c.Language()
	type cmd struct {
		Command     string `json:"command"`
		Description string `json:"description"`
	}
	var list []cmd
	for _, e := range commandList() {
		list = append(list, cmd{Command: e.name, Description: tr(lang, e.desc)})
	}
	data, err := json.Marshal(list)
	if err != nil {
		return
	}
	form := url.Values{}
	form.Set("commands", string(data))
	_, _ = call(c, "setMyCommands", form)
}

// commandEntry is one row of the slash-command menu.
type commandEntry struct{ name, desc string }

// commandList is the single source of truth for what the bot understands: the
// slash menu, the help screen and the command router all read it, so a command
// cannot exist in one and be missing from another.
func commandList() []commandEntry {
	return []commandEntry{
		{"status", "every tunnel: state, ports, traffic"},
		{"tunnels", "start, stop or restart a tunnel"},
		{"system", "processor, memory and disk"},
		{"alerts", "alert thresholds and switches"},
		{"health", "run every diagnostic check"},
		{"history", "recent alerts and bot actions"},
		{"backup", "send a full backup here as a file"},
		{"webui", "panel link and login code"},
		{"support", "project links and donations"},
		{"help", "what the bot can do"},
	}
}
