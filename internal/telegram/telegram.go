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
	// Admins are the accounts allowed to use the bot besides AdminID, which
	// stays the owner. An entry may be read-only: every screen, no actions.
	Admins []Admin `json:"admins,omitempty"`
	// Lang is the language the bot writes in: "en" or "fa". Empty means
	// English, so a config written before this existed keeps the wording it
	// has always had rather than changing language on an update.
	Lang string `json:"lang,omitempty"`
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
	// The report is one of the two places the bot writes unprompted, so it is
	// written in whatever language the operator chose.
	lang := Load().Language()
	var b strings.Builder
	tunnels := manage.List()
	if len(tunnels) == 0 {
		return tr(lang, "No tunnels configured.")
	}

	health := manage.AllHealth()
	for i, t := range tunnels {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(tunnelBlock(lang, t, health[t.Name]))
	}

	if manage.IsActive(app.WebUIService) {
		pw, port := webPanelInfo()
		fmt.Fprintf(&b, "\n"+tr(lang, "Web Panel")+" : %s\n"+tr(lang, "Password")+" : %s\n",
			code(fmt.Sprintf("http://%s:%d", manage.PublicIPv4(), port)), code(pw))
	}
	return b.String()
}

// stateIcon is the one place a health state becomes a colour, so the list, the
// detail screen and the report can never disagree about what green means.
func stateIcon(state string) string {
	switch state {
	case "online":
		return "🟢"
	case "offline":
		return "🟡"
	}
	return "🔴"
}

// tunnelBlock renders one tunnel.
func tunnelBlock(lang string, t manage.Tunnel, h manage.Health) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s ", stateIcon(h.State))
	if f := tunnelFlag(t); f != "" {
		fmt.Fprintf(&b, "%s ", f)
	}
	fmt.Fprintf(&b, "%s [ %s ]", esc(t.Name), esc(strings.ToUpper(t.Transport)))
	if p := manage.PresetLabel(t.Name); p != "" {
		fmt.Fprintf(&b, " [ %s ]", esc(p))
	}
	b.WriteString("\n")

	// See the same block in tunnels.go: where the tunnel's socket is and
	// whether this side holds the ports are two questions, and a direct
	// tunnel answers them with different sides.
	if manage.DialsOut(t) {
		fmt.Fprintf(&b, tr(lang, "Server")+" : %s\n", code(t.Addr))
	} else {
		fmt.Fprintf(&b, tr(lang, "Tunnel Port")+" : %s\n", code(portOf(t.Addr)))
	}
	if manage.HoldsPorts(t) {
		if ports := manage.VisiblePorts(t.Ports, manage.TunnelToken(t.Name)); len(ports) > 0 {
			fmt.Fprintf(&b, tr(lang, "Forwarded Port")+" : %s\n", code(strings.Join(ports, ", ")))
		}
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
	// A side that dials out already knows where the far end is: it is the
	// address it was told to reach.
	if manage.DialsOut(t) {
		host, _, err := net.SplitHostPort(t.Addr)
		if err != nil {
			return ""
		}
		return host
	}
	// A listening side does not know its peer from its own config; the engine
	// records it while the tunnel is up.
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
	lang := Load().Language()
	s := sysstat.Get()
	var out strings.Builder

	out.WriteString(b("🖥 "+tr(lang, "System")) + "\n\n")
	if s.OS != "" {
		fmt.Fprintf(&out, "OS : %s\n", esc(s.OS))
	}
	fmt.Fprintf(&out, "UpTime : %s\n\n", esc(sysstat.HumanDuration(s.Uptime)))

	fmt.Fprintf(&out, "<code>%s</code> CPU %.1f%%\n", bar(s.CPUPercent), s.CPUPercent)
	fmt.Fprintf(&out, "<code>%s</code> Memory %.1f%%\n", bar(s.MemPercent), s.MemPercent)
	fmt.Fprintf(&out, "<code>%s</code> Disk %.1f%%\n", bar(s.DiskPercent), s.DiskPercent)
	return out.String()
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

// helpText lists what the bot understands. The command list is generated from
// commandList so the help screen and Telegram's own slash menu are the same
// list, written once.
func helpText(lang string) string {
	var out strings.Builder
	out.WriteString(b("🎒 Backpack") + " " + esc(app.Version) + "\n\n")
	for _, e := range commandList() {
		fmt.Fprintf(&out, "/%s — %s\n", e.name, esc(tr(lang, e.desc)))
	}
	out.WriteString("\n" + esc(tr(lang, "Alerts arrive on their own when a threshold is crossed, a tunnel changes state, or a new version is released.")))
	return out.String()
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
	text := StatusText()
	// The owner's delivery is the one that decides success — the cron job has
	// somewhere to report a failure. The other admins are told too, but a
	// second admin's blocked chat is not a reason to call the report failed.
	err := send(c, c.AdminID, text)
	for _, id := range c.recipients()[1:] {
		_ = send(c, id, text)
	}
	return explainSendFailure(c, err)
}

// SendToAdmin delivers one message to the configured admin chat. Used by the
// web panel for login codes; the relay handling is the same as every other
// message the bot sends.
func SendToAdmin(text string) error {
	c := Load()
	if c.Token == "" || c.AdminID == "" {
		return fmt.Errorf("telegram bot is not configured")
	}
	return explainSendFailure(c, sendPlain(c, c.AdminID, text))
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

// --- interactive bot (inline buttons) --------------------------------------

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
	Callback *struct {
		ID      string     `json:"id"`
		Data    string     `json:"data"`
		Message *tgMessage `json:"message"`
		From    tgUser     `json:"from"`
	} `json:"callback_query"`
}

type tgMessage struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	From      tgUser `json:"from"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
}

type tgUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// RunBot long-polls Telegram for button presses and commands, and responds.
// It runs only where the bot is configured (a single node — normally Iran), so
// there is no getUpdates conflict. Safe to start unconditionally.
func RunBot(ctx context.Context) {
	// The offset is read back from disk rather than starting at zero.
	//
	// Telegram redelivers every update that has not been confirmed, and the
	// confirmation is the offset on the *next* poll. A process that is killed
	// after handling a press but before polling again therefore used to replay
	// it on startup — harmless when every button was a read-only screen, and
	// not harmless at all now that one of them restarts a tunnel.
	offset := loadOffset()
	var announced string

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
		// Registering the slash menu needs a token, so it cannot happen at
		// startup; and it must happen again if the language changes, because the
		// descriptions are translated. Keyed on both so it costs one call.
		if key := c.Token + "|" + c.Language(); key != announced {
			setMyCommands(c)
			announced = key
		}

		updates, err := getUpdates(c, offset)
		if err != nil {
			sleepCtx(ctx, 5*time.Second)
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			saveOffset(offset)
			handleUpdate(c, u)
		}
	}
}

// offsetFile is where the last confirmed update id is kept; a variable so tests
// can point it somewhere writable.
var offsetFile = app.ConfigDir + "/telegram-offset"

func loadOffset() int64 {
	data, err := os.ReadFile(offsetFile)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func saveOffset(n int64) {
	_ = app.WriteFileAtomic(offsetFile, []byte(strconv.FormatInt(n, 10)), 0600)
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

// handleUpdate dispatches one incoming update.
func handleUpdate(c Config, u tgUpdate) {
	if u.Callback != nil {
		user := strconv.FormatInt(u.Callback.From.ID, 10)
		if !c.isAdmin(user) {
			answerCallback(c, u.Callback.ID, tr(c.Language(), "Not authorised."), true)
			return
		}
		chat, msgID := c.AdminID, int64(0)
		if u.Callback.Message != nil {
			chat = strconv.FormatInt(u.Callback.Message.Chat.ID, 10)
			msgID = u.Callback.Message.MessageID
		}
		r := route(c, u.Callback.From, u.Callback.Data)
		answerCallback(c, u.Callback.ID, r.toast, r.alert)
		r.deliver(c, chat, msgID)
		return
	}

	if u.Message == nil {
		return
	}
	user := strconv.FormatInt(u.Message.From.ID, 10)
	if !c.isAdmin(user) {
		return
	}
	chat := strconv.FormatInt(u.Message.Chat.ID, 10)
	if chat == "0" {
		chat = c.AdminID
	}

	name := command(u.Message.Text)
	data, ok := commandRoute(name)
	if !ok {
		data = "nav:home"
	}
	// A typed command always answers with a new message: there is nothing on
	// screen to edit, and editing the user's own command is not possible.
	r := route(c, u.Message.From, data)
	r.edit = false
	r.deliver(c, chat, 0)
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

// commandRoute maps a slash command onto the callback data of the screen it
// opens. Anything not listed here is not a command.
func commandRoute(name string) (string, bool) {
	switch name {
	case "status", "overview":
		return "nav:overview", true
	case "tunnels":
		return "nav:tunnels", true
	case "system":
		return "nav:system", true
	case "alerts":
		return "nav:alerts", true
	case "health":
		return "nav:health", true
	case "history":
		return "nav:history", true
	case "webui":
		return "nav:webui", true
	case "support":
		return "nav:support", true
	case "backup":
		return "act:backup", true
	case "help", "start":
		return "nav:home", true
	}
	return "", false
}

func webUIText(lang string) string {
	pw, port := webPanelInfo()
	return b("🖥 "+tr(lang, "Web Panel")) + "\n\n" +
		tr(lang, "Address") + " : " + code(fmt.Sprintf("http://%s:%d", manage.PublicIPv4(), port)) + "\n" +
		tr(lang, "Password") + " : " + code(pw)
}

func supportText(lang string) string {
	return b("💛 "+tr(lang, "Support")) + "\n\n" +
		"GitHub : https://github.com/AminMGMT\n" +
		"Channel : https://t.me/BlackProtocols\n\n" +
		"🔺 Tron [ TRX ] :\n" + code("TTzuUAtsEsrLgNpFVLNTyLVJVRRFNWESYc") + "\n\n" +
		"💠 USDT [ BEP20 ] :\n" + code("0xc112AE9bfF7c59dEcFb34E988A397848D3093E82") + "\n\n" +
		"💎 Gram [ TON ] :\n" + code("UQD9g40QubAICJ6zPqegtCY7s-joMx2DB8aIqA0xF1aHoCDs")
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
