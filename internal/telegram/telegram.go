// Package telegram sends periodic tunnel status reports to a Telegram admin and
// runs an interactive bot with Status / Web UI / Support buttons.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/geo"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/schedule"
	"github.com/backpack/backpack/internal/sysstat"
)

// menuKeyboard is the inline-button keyboard attached to bot messages. Two
// buttons per row so the labels stay readable on a phone.
const menuKeyboard = `{"inline_keyboard":[` +
	`[{"text":"📊 Status","callback_data":"status"},{"text":"🖥 System","callback_data":"system"}],` +
	`[{"text":"🔐 Backup","callback_data":"backup"},{"text":"💛 Support","callback_data":"support"}]]}`

const cronMarker = "backpack-telegram"

// Config is the persisted Telegram bot configuration.
type Config struct {
	Token         string `json:"token"`
	AdminID       string `json:"admin_id"`
	IntervalHours int    `json:"interval_hours"`
	// ViaTunnel, if set, routes Telegram messages through that tunnel's peer
	// (used when this server — e.g. Iran — cannot reach Telegram directly).
	ViaTunnel string `json:"via_tunnel"`
	// SocksPort is the tunnel-exposed port the bot sends through, kept under
	// its original name so existing configs keep working.
	SocksPort int `json:"socks_port"`
	// Alerts controls threshold and tunnel-state notifications.
	Alerts AlertConfig `json:"alerts"`
}

// Load reads the saved config, returning a zero value if none exists.
//
// A file written before alerts existed has no "alerts" key at all. That is not
// the same as alerts being switched off, and the difference matters: decoding
// it straight into the struct would leave Enabled false and silently give every
// upgraded install a bot that never warns about anything. So the key is probed
// first, and its absence means "has never chosen" — which gets the defaults.
func Load() Config {
	var c Config
	data, err := os.ReadFile(app.TelegramConfig)
	if err != nil {
		// No config at all: a first-time setup starts from the defaults, so a
		// freshly configured bot alerts without having to be told to.
		c.Alerts = DefaultAlerts()
		return c
	}
	if json.Unmarshal(data, &c) != nil {
		c.Alerts = DefaultAlerts()
		return c
	}

	var probe struct {
		Alerts *AlertConfig `json:"alerts"`
	}
	if json.Unmarshal(data, &probe) == nil && probe.Alerts == nil {
		c.Alerts = DefaultAlerts()
	}
	c.Alerts = c.Alerts.normalise()
	return c
}

// Save persists the config to disk.
func Save(c Config) error {
	data, _ := json.MarshalIndent(c, "", "  ")
	// Atomic: the monitor service re-reads this file on every alert cycle, so a
	// half-written config would be read as "bot not configured".
	return app.WriteFileAtomic(app.TelegramConfig, data, 0600)
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

// StatusText is the report the bot leads with, and deliberately the only screen
// most people will ever need.
//
// It used to be a one-line-per-tunnel summary, with the detail split across
// separate Tunnels and Metrics screens. Nobody wants to press three buttons to
// answer "is everything fine": the interesting facts are few enough to fit
// together, so they do.
func StatusText() string {
	var b strings.Builder
	tunnels := manage.List()
	if len(tunnels) == 0 {
		return "No tunnels configured."
	}

	health := manage.AllHealth()
	for i, t := range tunnels {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(tunnelBlock(t, health[t.Name]))
	}

	if manage.IsActive(app.WebUIService) {
		pw, port := webPanelInfo()
		fmt.Fprintf(&b, "\nWeb Panel : http://%s:%d\nPassword : %s\n",
			manage.PublicIPv4(), port, pw)
	}
	return b.String()
}

// tunnelBlock renders one tunnel.
func tunnelBlock(t manage.Tunnel, h manage.Health) string {
	var b strings.Builder

	icon := "🔴"
	switch h.State {
	case "online":
		icon = "🟢"
	case "offline":
		icon = "🟡"
	}

	head := icon + " "
	if f := tunnelFlag(t); f != "" {
		head += f + " "
	}
	head += t.Name + " [ " + strings.ToUpper(t.Transport) + " ]"
	if p := manage.PresetLabel(t.Name); p != "" {
		head += " [ " + p + " ]"
	}
	b.WriteString(head + "\n")

	if t.Role == "server" {
		fmt.Fprintf(&b, "Tunnel Port : %s\n", portOf(t.Addr))
		if ports := manage.VisiblePorts(t.Ports, manage.TunnelToken(t.Name)); len(ports) > 0 {
			fmt.Fprintf(&b, "Forwarded Port : %s\n", strings.Join(ports, ", "))
		}
	} else {
		fmt.Fprintf(&b, "Server : %s\n", t.Addr)
	}

	if snap, err := metrics.Read(app.ConfigDir, t.Name); err == nil {
		total := snap.BytesOut + snap.BytesIn
		fmt.Fprintf(&b, "↑ %s | ↓ %s | Σ %s\n",
			sysstat.HumanBytes(snap.BytesOut),
			sysstat.HumanBytes(snap.BytesIn),
			sysstat.HumanBytes(total))
	}
	return b.String()
}

// tunnelFlag returns the flag emoji for wherever the tunnel's far end is.
//
// Detected from the peer address rather than configured, so it is right without
// anybody maintaining it — and simply absent when the address cannot be
// resolved, which is better than a wrong flag.
func tunnelFlag(t manage.Tunnel) string {
	ip := peerIP(t)
	if ip == "" {
		return ""
	}
	g := geo.Lookup(ip)
	if g == nil || g.Code == "" {
		return ""
	}
	return flagEmoji(g.Code)
}

// peerIP finds the address of the tunnel's far end.
func peerIP(t manage.Tunnel) string {
	if t.Role == "client" {
		host, _, err := net.SplitHostPort(t.Addr)
		if err != nil {
			return ""
		}
		return host
	}
	// A server does not know its peer from its own config; the engine records
	// it while the control channel is up.
	snap, err := metrics.Read(app.ConfigDir, t.Name)
	if err != nil || snap.Peer == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(snap.Peer)
	if err != nil {
		return snap.Peer
	}
	return host
}

// flagEmoji turns an ISO 3166-1 alpha-2 code into its flag, which is simply the
// two letters written as regional indicator symbols.
func flagEmoji(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 {
		return ""
	}
	// Providers use these for "could not tell". They are valid letter pairs, so
	// they would render as two letter boxes rather than a flag — worse than
	// showing nothing, because it looks like a broken flag.
	switch code {
	case "XX", "ZZ", "T1", "AP", "EU":
		return ""
	}
	const base = 0x1F1E6 // REGIONAL INDICATOR SYMBOL LETTER A
	r := []rune{}
	for _, c := range code {
		if c < 'A' || c > 'Z' {
			return ""
		}
		r = append(r, rune(base+int(c-'A')))
	}
	return string(r)
}

// portOf returns just the port from a host:port address.
func portOf(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return addr
}

// SystemText reports how loaded the machine is.
//
// Trimmed to what someone actually acts on. Core count, load average and total
// byte figures were dropped: they are either constant, or they say the same
// thing as the percentage directly above them.
func SystemText() string {
	s := sysstat.Get()
	var b strings.Builder

	if s.OS != "" {
		fmt.Fprintf(&b, "OS : %s\n", s.OS)
	}
	fmt.Fprintf(&b, "UpTime : %s\n\n", sysstat.HumanDuration(s.Uptime))

	fmt.Fprintf(&b, "%s CPU %.1f%%\n", bar(s.CPUPercent), s.CPUPercent)
	fmt.Fprintf(&b, "%s Memory %.1f%%\n", bar(s.MemPercent), s.MemPercent)
	fmt.Fprintf(&b, "%s Disk %.1f%%\n", bar(s.DiskPercent), s.DiskPercent)
	return b.String()
}

// bar draws a ten-segment meter. A number is precise; a bar is glanceable, and
// on a phone the difference decides whether the message gets read.
func bar(pct float64) string {
	const width = 10
	filled := int(pct/100*width + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

// AlertsText reports the current alert settings.
func AlertsText() string {
	return Load().Alerts.Summary()
}

// helpText lists what the bot understands.
func helpText() string {
	return "🎒 Backpack\n\n" +
		"/status — every tunnel: state, ports, traffic\n" +
		"/system — processor, memory and disk\n" +
		"/backup — send a full backup here as a file\n" +
		"/alerts — current alert thresholds\n" +
		"/webui — panel link and login code\n" +
		"/support — project links and donations\n\n" +
		"Alerts arrive on their own when a threshold is crossed, a tunnel " +
		"changes state, or a new version is released."
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
	return explainSendFailure(c, send(c, c.AdminID, StatusText()))
}

// SendTest sends a one-off confirmation message.
//
// It retries briefly on the transport errors a not-yet-ready relay produces.
// Resolving the relay can restart the tunnel — to add the forward port, or to
// move an older mapping onto loopback — and the far end has to reconnect before
// the first request can cross. The local port is listening the instant the
// server restarts, so the send fires into a tunnel whose peer has not come back
// yet and gets an EOF that clears itself a second or two later. Reporting that
// first attempt as a failure is how a working setup looks broken right after it
// is configured.
func SendTest(c Config) error {
	const msg = "✅ Backpack is connected. You will receive status reports here."

	// Only a relayed send has the restart-then-reconnect race; a direct send
	// that fails is failing for a real reason and should say so at once.
	if c.ViaTunnel == "" {
		return explainSendFailure(c, send(c, c.AdminID, msg))
	}

	var err error
	deadline := time.Now().Add(20 * time.Second)
	for {
		if err = send(c, c.AdminID, msg); err == nil {
			return nil
		}
		if time.Now().After(deadline) || !isRelayWarmingUp(err) {
			return explainSendFailure(c, err)
		}
		time.Sleep(2 * time.Second)
	}
}

// isRelayWarmingUp reports whether an error is the kind a tunnel that has just
// restarted throws while its peer reconnects — as opposed to a settled failure
// like the wrong sort of proxy answering, which no amount of waiting fixes.
func isRelayWarmingUp(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{"EOF", "connection refused", "reset by peer", "broken pipe", "i/o timeout"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// botClient returns an HTTP client that reaches Telegram directly, or (when
// ViaTunnel is set) through a tunnel: a loopback port forwarded straight to
// api.telegram.org, with the peer making the outbound connection — so a server
// with no Telegram access (e.g. Iran) can send via its peer (e.g. kharej).
// The URL still names api.telegram.org, so TLS is verified against it and the
// tunnel carries a stream it cannot read.
func botClient(c Config, timeout time.Duration) (*http.Client, error) {
	// Resolved per call rather than read from the config, so on automatic mode a
	// tunnel going down switches the bot to another without intervention.
	name, port, err := resolveRelay(c)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return &http.Client{Timeout: timeout}, nil // direct
	}
	if port == 0 {
		return nil, fmt.Errorf("no relay port configured for tunnel %q", name)
	}
	return tunnelledClient(port, timeout), nil
}

// tunnelledClient sends every Telegram request through a local port that the
// tunnel forwards straight to api.telegram.org.
//
// Only the dial is redirected. The URL, the Host header and the TLS handshake
// are untouched, so the certificate is still checked against api.telegram.org —
// the tunnel carries an encrypted stream it cannot read or alter.
func tunnelledClient(port int, timeout time.Duration) *http.Client {
	local := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				// Everything the bot asks for goes to the API host; anything
				// else would be a bug rather than something to forward blindly.
				if addr == manage.TelegramHost {
					addr = local
				}
				return dialer.DialContext(ctx, network, addr)
			},
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 15 * time.Second,
		},
	}
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

// screen maps a button or command name to the text it produces. Both entry
// points resolve through here so a button and its command can never drift apart.
func screen(name string) (string, bool) {
	switch name {
	case "status":
		return StatusText(), true
	case "system":
		return SystemText(), true
	case "alerts":
		return AlertsText(), true
	case "webui":
		return webUIText(), true
	case "support":
		return supportText(), true
	case "help", "start":
		return helpText(), true
	}
	return "", false
}

func handleUpdate(c Config, u tgUpdate) {
	if u.Callback != nil {
		if strconv.FormatInt(u.Callback.From.ID, 10) == c.AdminID {
			respond(c, u.Callback.Data)
		}
		answerCallback(c, u.Callback.ID)
		return
	}
	if u.Message == nil || strconv.FormatInt(u.Message.From.ID, 10) != c.AdminID {
		return
	}
	respond(c, command(u.Message.Text))
}

// respond handles one button press or command.
func respond(c Config, name string) {
	// Backup sends a file rather than a message, so it cannot go through
	// screen(). It is also slow enough to be worth acknowledging first.
	if name == "backup" {
		send(c, c.AdminID, "🔐 Preparing your backup…")
		if err := sendBackup(c); err != nil {
			send(c, c.AdminID, "Backup failed: "+err.Error())
		}
		return
	}
	if text, ok := screen(name); ok {
		send(c, c.AdminID, text)
		return
	}
	send(c, c.AdminID, helpText())
}

// command extracts a bare command name from a message. Telegram appends the bot
// username in groups ("/status@mybot"), and clients may send trailing arguments,
// so both are stripped before matching.
func command(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return ""
	}
	text = strings.TrimPrefix(text, "/")
	if i := strings.IndexAny(text, " \t\n"); i >= 0 {
		text = text[:i]
	}
	if i := strings.Index(text, "@"); i >= 0 {
		text = text[:i]
	}
	return strings.ToLower(text)
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
	return "GitHub : https://github.com/AminMGMT\n" +
		"Channel : https://t.me/BlackProtocols\n\n" +
		"🔺 Tron [ TRX ] :\n" +
		"TTzuUAtsEsrLgNpFVLNTyLVJVRRFNWESYc\n\n" +
		"💠 USDT [ BEP20 ] :\n" +
		"0xc112AE9bfF7c59dEcFb34E988A397848D3093E82\n\n" +
		"💎 Gram [ TON ] :\n" +
		"UQD9g40QubAICJ6zPqegtCY7s-joMx2DB8aIqA0xF1aHoCDs"
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// sendBackup builds a backup and sends it to the admin as a file.
//
// Streamed straight into the upload rather than written to disk first: the
// archive holds every token and the panel password, and a copy left in /tmp is
// a copy someone has to remember to delete.
func sendBackup(c Config) error {
	var buf bytes.Buffer
	if err := manage.WriteBackup(&buf); err != nil {
		return fmt.Errorf("could not build the backup: %w", err)
	}

	name := fmt.Sprintf("backpack-backup-%s.tar.gz", time.Now().Format("2006-01-02-1504"))
	caption := "🔐 Full backup — every tunnel and token, the panel password, " +
		"Telegram settings and certificates.\n\nKeep it private: anyone with this " +
		"file can connect to your tunnels."

	return sendDocument(c, name, caption, buf.Bytes())
}

// sendDocument uploads a file to the admin chat.
func sendDocument(c Config, filename, caption string, data []byte) error {
	client, err := botClient(c, 120*time.Second)
	if err != nil {
		return err
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("chat_id", c.AdminID)
	_ = w.WriteField("caption", caption)

	part, err := w.CreateFormFile("document", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", c.Token)
	req, err := http.NewRequest(http.MethodPost, endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram rejected the upload (status %d)", resp.StatusCode)
	}
	return nil
}
