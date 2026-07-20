package telegram

import (
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
	got := supportText()
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

// Status absorbed the Tunnels and Metrics screens, so help must not advertise
// commands that no longer exist — and must mention the one that replaced them.
func TestHelpMatchesWhatTheBotActuallyDoes(t *testing.T) {
	help := helpText()

	for _, cmd := range []string{"status", "system", "backup", "alerts", "webui", "support"} {
		if !strings.Contains(help, "/"+cmd) {
			t.Errorf("/%s is not listed in help", cmd)
		}
		// backup is an action rather than a screen, so it is exempt here.
		if cmd == "backup" {
			continue
		}
		if _, ok := screen(cmd); !ok {
			t.Errorf("help lists /%s but it does not resolve to a screen", cmd)
		}
	}
	for _, gone := range []string{"/tunnels", "/metrics"} {
		if strings.Contains(help, gone) {
			t.Errorf("help still lists %s, which Status replaced", gone)
		}
	}
}

// Every button must do something. A dead button looks like the bot has hung.
func TestEveryButtonIsHandled(t *testing.T) {
	for _, name := range []string{"status", "system", "backup", "support"} {
		if !strings.Contains(menuKeyboard, `"callback_data":"`+name+`"`) {
			t.Errorf("%q is not on the keyboard", name)
		}
		if name == "backup" {
			continue // handled as an action in respond()
		}
		if _, ok := screen(name); !ok {
			t.Errorf("button %q resolves to nothing", name)
		}
	}
	// The removed screens must not linger on the keyboard.
	for _, gone := range []string{"tunnels", "metrics"} {
		if strings.Contains(menuKeyboard, `"callback_data":"`+gone+`"`) {
			t.Errorf("the keyboard still offers %q", gone)
		}
	}
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
