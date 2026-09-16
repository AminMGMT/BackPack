package telegram

import (
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
