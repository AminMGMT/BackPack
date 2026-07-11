// Package telegram sends periodic tunnel status reports to a Telegram admin and
// runs an interactive bot with Status / Web UI / Support buttons.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/schedule"
	"github.com/backpack/backpack/internal/socks"
)

// menuKeyboard is the inline-button keyboard attached to bot messages.
const menuKeyboard = `{"inline_keyboard":[` +
	`[{"text":"📊 Status","callback_data":"status"}],` +
	`[{"text":"🖥 Web UI","callback_data":"webui"}],` +
	`[{"text":"💬 Support","callback_data":"support"}]]}`

const cronMarker = "backpack-telegram"

// Config is the persisted Telegram bot configuration.
type Config struct {
	Token         string `json:"token"`
	AdminID       string `json:"admin_id"`
	IntervalHours int    `json:"interval_hours"`
	// ViaTunnel, if set, routes Telegram messages through that tunnel's peer
	// (used when this server — e.g. Iran — cannot reach Telegram directly).
	ViaTunnel string `json:"via_tunnel"`
	// SocksPort is the tunnel-exposed port that forwards to the peer's SOCKS5
	// proxy; used together with ViaTunnel.
	SocksPort int `json:"socks_port"`
}

// Load reads the saved config, returning a zero value if none exists.
func Load() Config {
	var c Config
	data, err := os.ReadFile(app.TelegramConfig)
	if err != nil {
		return c
	}
	json.Unmarshal(data, &c)
	return c
}

// Save persists the config to disk.
func Save(c Config) error {
	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(app.TelegramConfig, data, 0600)
}

// Configure persists settings and (re)schedules the periodic report.
func Configure(c Config) error {
	if err := Save(c); err != nil {
		return err
	}
	return schedule.SetCron(cronMarker, schedule.HourlySpec(c.IntervalHours), app.BinPath+" --telegram-report")
}

// Disable removes the scheduled report.
func Disable() error {
	return schedule.RemoveCron(cronMarker)
}

// IntervalHours returns the currently scheduled report interval (0 = off).
func IntervalHours() int {
	return schedule.GetIntervalHours(cronMarker)
}

// StatusText builds a human-readable status report of all tunnels.
func StatusText() string {
	var b strings.Builder
	hostname, _ := os.Hostname()
	b.WriteString("📦 Backpack Status Report\n")
	fmt.Fprintf(&b, "🖥 Host: %s\n", hostname)
	fmt.Fprintf(&b, "🌐 IPv4: %s\n", manage.PublicIPv4())
	fmt.Fprintf(&b, "🕐 %s\n\n", time.Now().Format("2006-01-02 15:04:05 MST"))

	tunnels := manage.List()
	if len(tunnels) == 0 {
		b.WriteString("No tunnels configured.")
		return b.String()
	}
	for _, t := range tunnels {
		icon := "🔴"
		if manage.IsActive(t.Service) {
			icon = "🟢"
		}
		detail := t.Addr
		if t.Role == "server" && len(t.Ports) > 0 {
			detail = strings.Join(t.Ports, ",")
		}
		fmt.Fprintf(&b, "%s %s [%s/%s] — %s\n", icon, t.Name, t.Role, t.Transport, detail)
	}

	// Include the web panel URL and login code so the admin always has them.
	if manage.IsActive(app.WebUIService) {
		pw, port := webPanelInfo()
		fmt.Fprintf(&b, "\n🖥 Web Panel: http://%s:%d\n🔑 Password: %s\n",
			manage.PublicIPv4(), port, pw)
	}
	return b.String()
}

// webPanelInfo reads the web-panel password and port straight from disk to
// avoid importing the webui package (which would create an import cycle).
func webPanelInfo() (password string, port int) {
	port = app.WebUIPort
	data, err := os.ReadFile(app.WebUIConfig)
	if err != nil {
		return "", port
	}
	var c struct {
		Password string `json:"password"`
		Port     int    `json:"port"`
	}
	if json.Unmarshal(data, &c) == nil {
		password = c.Password
		if c.Port > 0 {
			port = c.Port
		}
	}
	return password, port
}

// SendStatusNow sends the current status to the configured admin. Called by
// the `backpack --telegram-report` cron job.
func SendStatusNow() error {
	c := Load()
	if c.Token == "" || c.AdminID == "" {
		return fmt.Errorf("telegram bot is not configured")
	}
	return send(c, c.AdminID, StatusText())
}

// SendTest sends a one-off confirmation message.
func SendTest(c Config) error {
	return send(c, c.AdminID, "✅ Backpack is connected. You will receive status reports here.")
}

// botClient returns an HTTP client that reaches Telegram directly, or (when
// ViaTunnel is set) through the SOCKS5 proxy on the tunnel peer — so a server
// with no Telegram access (e.g. Iran) can send via its peer (e.g. kharej).
func botClient(c Config, timeout time.Duration) (*http.Client, error) {
	if c.ViaTunnel == "" {
		return &http.Client{Timeout: timeout}, nil
	}
	token := manage.TunnelToken(c.ViaTunnel)
	if token == "" {
		return nil, fmt.Errorf("tunnel %q not found", c.ViaTunnel)
	}
	if c.SocksPort == 0 {
		return nil, fmt.Errorf("relay port not configured for tunnel %q", c.ViaTunnel)
	}
	proxy := net.JoinHostPort("127.0.0.1", strconv.Itoa(c.SocksPort))
	return socks.HTTPClient(proxy, "backpack", token, timeout), nil
}

// send delivers a message (with the button menu attached) to the chat.
func send(c Config, chatID, text string) error {
	client, err := botClient(c, 20*time.Second)
	if err != nil {
		return err
	}
	return postMessage(client, c.Token, chatID, text, menuKeyboard)
}

// postMessage posts a message via the Telegram Bot API, optionally with an
// inline keyboard (reply_markup).
func postMessage(client *http.Client, botToken, chatID, text, replyMarkup string) error {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	form := url.Values{}
	form.Set("chat_id", chatID)
	form.Set("text", text)
	if replyMarkup != "" {
		form.Set("reply_markup", replyMarkup)
	}
	resp, err := client.PostForm(endpoint, form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API returned status %d", resp.StatusCode)
	}
	return nil
}

// --- interactive bot (inline buttons) --------------------------------------

type tgUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Text string `json:"text"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
	} `json:"message"`
	Callback *struct {
		ID   string `json:"id"`
		Data string `json:"data"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
	} `json:"callback_query"`
}

// RunBot long-polls Telegram for button presses and commands, and responds.
// It runs only where the bot is configured (a single node — normally Iran), so
// there is no getUpdates conflict. Safe to start unconditionally.
func RunBot(ctx context.Context) {
	var offset int64
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		c := Load()
		if c.Token == "" || c.AdminID == "" {
			sleepCtx(ctx, 15*time.Second)
			continue
		}
		updates, err := getUpdates(c, offset)
		if err != nil {
			sleepCtx(ctx, 5*time.Second)
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			handleUpdate(c, u)
		}
	}
}

func getUpdates(c Config, offset int64) ([]tgUpdate, error) {
	client, err := botClient(c, 40*time.Second)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?timeout=30&offset=%d", c.Token, offset)
	resp, err := client.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

func handleUpdate(c Config, u tgUpdate) {
	if u.Callback != nil {
		if strconv.FormatInt(u.Callback.From.ID, 10) == c.AdminID {
			switch u.Callback.Data {
			case "status":
				send(c, c.AdminID, StatusText())
			case "webui":
				send(c, c.AdminID, webUIText())
			case "support":
				send(c, c.AdminID, supportText())
			}
		}
		answerCallback(c, u.Callback.ID)
		return
	}
	if u.Message != nil && strconv.FormatInt(u.Message.From.ID, 10) == c.AdminID {
		send(c, c.AdminID, "🎒 Backpack — choose an option:")
	}
}

func answerCallback(c Config, id string) {
	client, err := botClient(c, 15*time.Second)
	if err != nil {
		return
	}
	form := url.Values{}
	form.Set("callback_query_id", id)
	if resp, err := client.PostForm(fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", c.Token), form); err == nil {
		resp.Body.Close()
	}
}

func webUIText() string {
	pw, port := webPanelInfo()
	return fmt.Sprintf("🖥 Web Panel\n\nURL:  http://%s:%d\nPassword:  %s", manage.PublicIPv4(), port, pw)
}

func supportText() string {
	return "💛 Support Backpack\n\n" +
		"GitHub:  https://github.com/AminMGMT\n" +
		"Channel:  https://t.me/BlackProtocols\n\n" +
		"🔺 Tron (TRX):\nTTzuUAtsEsrLgNpFVLNTyLVJVRRFNWESYc\n\n" +
		"💠 USDT (BEP20):\n0xc112AE9bfF7c59dEcFb34E988A397848D3093E82\n\n" +
		"💎 Toncoin (TON):\nUQD9g40QubAICJ6zPqegtCY7s-joMx2DB8aIqA0xF1aHoCDs"
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
