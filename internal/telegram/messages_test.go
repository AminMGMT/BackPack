package telegram

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestFlagEmoji(t *testing.T) {
	cases := map[string]string{
		"NL": "🇳🇱",
		"nl": "🇳🇱", // providers are inconsistent about case
		"DE": "🇩🇪",
		"IR": "🇮🇷",
		// Placeholders for "unknown" are valid letter pairs, so they would
		// render as letter boxes rather than a flag.
		"XX": "",
		"ZZ": "",
		"EU": "",
		// Malformed input must not produce mojibake.
		"":     "",
		"N":    "",
		"NLD":  "",
		"1A":   "",
		"  DE": "🇩🇪",
	}
	for in, want := range cases {
		if got := flagEmoji(in); got != want {
			t.Errorf("flagEmoji(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPortOf(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:1231": "1231",
		"[::]:8989":    "8989",
		"1.2.3.4:443":  "443",
		"no-port-here": "no-port-here",
		"[::1]:1080":   "1080",
	}
	for in, want := range cases {
		if got := portOf(in); got != want {
			t.Errorf("portOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// The system screen was trimmed on purpose. These assert what was dropped stays
// dropped, because "add one more useful line" is how it grew the first time.
func TestSystemTextIsTrimmed(t *testing.T) {
	got := SystemText()

	for _, want := range []string{"OS :", "UpTime :", "CPU", "Memory", "Disk"} {
		if !strings.Contains(got, want) {
			t.Errorf("system report is missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"Load average", "cores", "Host:"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("system report still carries %q, which was deliberately removed:\n%s", unwanted, got)
		}
	}
}

func TestSupportTextFormat(t *testing.T) {
	got := supportText(LangEN)
	for _, want := range []string{
		"GitHub : ", "Channel : ",
		"🔺 Tron [ TRX ] :",
		"💠 USDT [ BEP20 ] :",
		"💎 Gram [ TON ] :",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("support text is missing %q:\n%s", want, got)
		}
	}
}

// Help, Telegram's own slash menu and the router are generated from one list,
// so the thing to prove is that the list is actually wired at both ends: every
// command it advertises must be routable, and the help screen must show them
// all. A command in the menu that the router does not know is a button in the
// user's client that answers with the wrong screen.
func TestHelpMatchesWhatTheBotActuallyDoes(t *testing.T) {
	help := helpText(LangEN)

	for _, e := range commandList() {
		if !strings.Contains(help, "/"+e.name) {
			t.Errorf("/%s is not listed in help", e.name)
		}
		if _, ok := commandRoute(e.name); !ok {
			t.Errorf("/%s is offered but the router does not know it", e.name)
		}
	}
	// Metrics was folded into Overview and must not come back as a command.
	if strings.Contains(help, "/metrics") {
		t.Error("help still lists /metrics, which Overview replaced")
	}
}

// Every button must do something. A dead button looks like the bot has hung,
// and the way one appears is a typo in a callback string that no compiler
// checks — so every keyboard the bot can draw is walked here and every
// destination matched against what the router actually handles.
func TestEveryButtonRoutes(t *testing.T) {
	keyboards := map[string]string{
		"home":     homeKeyboard(LangEN),
		"overview": overviewKeyboard(LangEN),
		"alerts":   alertsScreen(Config{Alerts: DefaultAlerts()}, LangEN).keyboard,
		"tools":    toolsScreen(LangEN).keyboard,
		"history":  historyScreen(Config{}, LangEN).keyboard,
		"audit":    auditScreen(Config{}, LangEN).keyboard,
		"update":   updateScreen(Config{}, LangEN).keyboard,
		"confirm": confirmScreen(LangEN, "q", "d", "yes",
			"do:restartall::"+stamp(), "nav:tunnels").keyboard,
	}

	navs := map[string]bool{}
	for _, n := range navScreens {
		navs[n] = true
	}

	for where, markup := range keyboards {
		for _, data := range callbackData(t, markup) {
			verb, rest, _ := strings.Cut(data, ":")
			switch verb {
			case "nav":
				if !navs[rest] {
					t.Errorf("%s keyboard points at unknown screen %q", where, rest)
				}
			case "t", "act", "do", "alert", "snap", "diag", "noop":
				// Handled by route; the argument is validated at press time
				// because it names a tunnel or snapshot that may have gone.
			default:
				t.Errorf("%s keyboard has button %q, which the router ignores", where, data)
			}
		}
	}
}

// Every screen the router can serve must be reachable, or it is code nobody can
// get to. Reachable means a button somewhere or a typed command.
func TestEveryScreenIsReachable(t *testing.T) {
	reached := map[string]bool{"home": true} // where the bot starts

	for _, markup := range []string{
		homeKeyboard(LangEN), overviewKeyboard(LangEN),
		toolsScreen(LangEN).keyboard, historyScreen(Config{}, LangEN).keyboard,
		auditScreen(Config{}, LangEN).keyboard, updateScreen(Config{}, LangEN).keyboard,
		alertsScreen(Config{Alerts: DefaultAlerts()}, LangEN).keyboard,
	} {
		for _, data := range callbackData(t, markup) {
			if verb, rest, _ := strings.Cut(data, ":"); verb == "nav" {
				reached[rest] = true
			}
		}
	}
	for _, e := range commandList() {
		if data, ok := commandRoute(e.name); ok {
			if verb, rest, _ := strings.Cut(data, ":"); verb == "nav" {
				reached[rest] = true
			}
		}
	}

	for _, name := range navScreens {
		if !reached[name] {
			t.Errorf("screen %q exists but nothing leads to it", name)
		}
	}
}

// callbackData pulls every callback_data out of a reply_markup.
func callbackData(t *testing.T, markup string) []string {
	t.Helper()
	if markup == "" {
		return nil
	}
	var parsed struct {
		Keyboard [][]struct {
			Data string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(markup), &parsed); err != nil {
		t.Fatalf("keyboard is not valid JSON: %v\n%s", err, markup)
	}
	var out []string
	for _, row := range parsed.Keyboard {
		for _, b := range row {
			if b.Data != "" {
				out = append(out, b.Data)
			}
		}
	}
	return out
}

// The relay's failure mode is the most common thing that goes wrong with the
// bot, and the raw error names the wrong machine.
func TestRelayFailureNamesTheRightMachine(t *testing.T) {
	pinned := Config{Token: "x", AdminID: "1", ViaTunnel: "nl", SocksPort: 31138}

	// A refused local dial is a near-side problem: the tunnel is not exposing
	// the port. Blaming the peer here sent the user to the wrong server.
	refused := errors.New("dial tcp 127.0.0.1:31138: connect: connection refused")
	got := explainSendFailure(pinned, refused).Error()
	if !strings.Contains(got, "THIS server") {
		t.Errorf("a refused local dial must point at this machine:\n%s", got)
	}
	if strings.Contains(got, "OTHER server") {
		t.Errorf("a refused local dial wrongly blames the peer:\n%s", got)
	}

	// An EOF after the connection was made is a far-side problem.
	eof := errors.New(`Post "https://api.telegram.org/botX/sendMessage": EOF`)
	got = explainSendFailure(pinned, eof).Error()
	if !strings.Contains(got, "OTHER server") {
		t.Errorf("an EOF mid-stream should point at the peer:\n%s", got)
	}

	if explainSendFailure(pinned, nil) != nil {
		t.Error("success must not be turned into an error")
	}
}

// The bot reaches Telegram through a forwarded port, not a proxy on the peer.
// Nothing should be left depending on the old mechanism.
func TestBotDoesNotDependOnAPeerProxy(t *testing.T) {
	src, err := os.ReadFile("telegram.go")
	if err != nil {
		t.Skipf("cannot read telegram.go: %v", err)
	}
	if strings.Contains(string(src), "socks.HTTPClient") {
		t.Error("the bot still routes through a SOCKS proxy on the peer")
	}
}

// The stale-mapping case has its own wording, because it is what every install
// that used the old proxy relay hits on upgrade — and the raw error
// ("does not look like a TLS handshake") gives no hint that the fix is simply
// to reconfigure.
func TestStaleProxyMappingIsNamed(t *testing.T) {
	c := Config{Token: "x", AdminID: "1", ViaTunnel: "nl", SocksPort: 28454}
	err := errors.New("tls: first record does not look like a TLS handshake")

	got := explainSendFailure(c, err).Error()
	if !strings.Contains(got, "old proxy") {
		t.Errorf("a stale proxy mapping should be named as the cause:\n%s", got)
	}
	if !strings.Contains(got, "Configure") {
		t.Errorf("it should say how to fix it:\n%s", got)
	}
}
