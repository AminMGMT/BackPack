package telegram

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// "Read-only" has to mean read-only about secrets too.
//
// The split was written as "every screen, no actions", and route enforced it
// exactly that way: anything under "act:" or "do:" went through actionReply and
// its canWrite check, and every "nav:" screen was answered for anyone on the
// admin list. That reading holds for eleven of the twelve screens, which are
// readings — how much traffic, which tunnels are up, what the last alert said.
//
// nav:webui is not a reading. It prints the web panel's password in plain text,
// and the panel is root on the machine: every tunnel and its token, the backups,
// the updater, the bot's own settings. So the account that had deliberately been
// denied the bot's restart button could take the whole server by opening a
// different screen — and /webui got there with no button to notice.
func TestAReadOnlyAdminCannotReachThePanelPassword(t *testing.T) {
	c := Config{AdminID: "1", Admins: []Admin{{ID: "3", ReadOnly: true}}}
	readOnly := tgUser{ID: 3}

	for _, data := range []string{"nav:webui"} {
		r := route(c, readOnly, data)
		if r.toast == "" || !r.alert {
			t.Errorf("%s was answered for a read-only admin without refusing", data)
		}
		if strings.Contains(r.text, "Password") {
			t.Errorf("%s handed a read-only admin the panel password", data)
		}
	}

	// The same press from an account that may write still works, or the screen
	// has simply been broken rather than gated.
	if r := route(c, tgUser{ID: 1}, "nav:webui"); !strings.Contains(r.text, "Password") {
		t.Error("the owner can no longer see the panel password")
	}
}

// A typed command reaches the same screen by the same route, which is what
// makes one check enough. /webui was the way in that had no button.
func TestTheWebuiCommandIsGatedTheSameWay(t *testing.T) {
	data, ok := commandRoute("webui")
	if !ok {
		t.Fatal("/webui is no longer a command")
	}
	c := Config{AdminID: "1", Admins: []Admin{{ID: "3", ReadOnly: true}}}
	if r := route(c, tgUser{ID: 3}, data); strings.Contains(r.text, "Password") {
		t.Error("/webui handed a read-only admin the panel password")
	}
}

// Every screen named as holding a secret has to be a screen that exists, or the
// gate is guarding a typo.
func TestEverySecretScreenIsARealScreen(t *testing.T) {
	for name := range secretScreens {
		found := false
		for _, s := range navScreens {
			if s == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("secretScreens names %q, which is not a screen", name)
		}
	}
}

// And the readings stay open to everyone on the admin list — a read-only admin
// exists in order to look at things.
func TestAReadOnlyAdminStillSeesTheReadings(t *testing.T) {
	c := Config{AdminID: "1", Admins: []Admin{{ID: "3", ReadOnly: true}}}
	for _, screen := range navScreens {
		if secretScreens[screen] {
			continue
		}
		if r := route(c, tgUser{ID: 3}, "nav:"+screen); r.alert {
			t.Errorf("nav:%s was refused to a read-only admin", screen)
		}
	}
}

// The panel password must not reach a screen that is not the gated one.
//
// Gating nav:webui closed the door that was written as a door. It was not the
// only way in: StatusText carries the report the bot leads with, and that text
// is the Overview screen — answered for anyone on the admin list, and what the
// bot opens on — as well as the scheduled report, which is sent unprompted to
// every recipient on a timer. Both reach a read-only admin, so the credential
// was in front of them before they pressed anything, and arrived again by
// itself every few hours.
func TestTheStatusReportDoesNotCarryThePanelPassword(t *testing.T) {
	pw := withPanelConfig(t)
	// panelLine rather than StatusText: StatusText returns "No tunnels
	// configured." on a machine with none, which is every machine this suite
	// runs on, and a check on the whole report would pass without ever reaching
	// the line under test.
	line := panelLine(LangEN)
	if strings.Contains(line, pw) {
		t.Errorf("the status report carries the web panel's password — it is the Overview "+
			"screen, which any admin can open, and the scheduled report, which arrives "+
			"without being asked for:\n%s", line)
	}
	// It still has to say where the panel is, or removing the password has
	// removed the point of the line.
	if !strings.Contains(line, "://") {
		t.Errorf("the status report no longer says where the panel is:\n%s", line)
	}
}

// The overview a read-only admin is served must not carry it either, whatever
// StatusText happens to contain.
func TestAReadOnlyAdminsOverviewCarriesNoPassword(t *testing.T) {
	pw := withPanelConfig(t)
	c := Config{AdminID: "1", Admins: []Admin{{ID: "3", ReadOnly: true}}}
	if r := route(c, tgUser{ID: 3}, "nav:overview"); strings.Contains(r.text, pw) {
		t.Error("the Overview screen hands a read-only admin the panel password")
	}
	// And the same for the owner's, because the point is that this credential
	// lives on one gated screen rather than on whichever screen happens to
	// mention the panel.
	if r := route(c, tgUser{ID: 1}, "nav:overview"); strings.Contains(r.text, pw) {
		t.Error("the Overview screen carries the panel password, so the gate on the " +
			"Web Panel screen guards a door with a window beside it")
	}
}

// The address it does carry has to be one that answers.
//
// It was built as http://IP:PORT. A panel behind TLS refuses the plain scheme,
// and every panel is served under a secret path segment and answers nothing
// outside it — so the bot's own Web Panel screen printed an address that 404s.
func TestThePanelAddressTheBotGivesOutIsComplete(t *testing.T) {
	withPanelConfig(t)
	_, url := webPanelInfo()
	if url == "" {
		t.Fatal("the bot has no address for the panel at all")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		t.Errorf("the panel address %q has no scheme", url)
	}
	// Scheme, host:port and a trailing slash at minimum; a base path when one is
	// configured, which is what the plain http://IP:PORT form could never carry.
	if !strings.HasSuffix(url, "/") {
		t.Errorf("the panel address %q does not end at a path the panel serves", url)
	}
}

// withPanelConfig puts a panel in front of the bot — behind TLS, on a secret
// path, with a password nothing else on the machine would produce — and returns
// that password. Every check above needs a panel to exist, and the machine
// running the suite has none.
func withPanelConfig(t *testing.T) string {
	t.Helper()
	const password = "panel-password-that-must-not-leak"
	path := filepath.Join(t.TempDir(), "webui.json")
	body := `{"password":"` + password + `","port":8443,"base_path":"x7Kq2pSecret",` +
		`"https":true,"tls_domain":"panel.example.ir"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	old := webUIConfigPath
	webUIConfigPath = path
	t.Cleanup(func() { webUIConfigPath = old })
	return password
}
