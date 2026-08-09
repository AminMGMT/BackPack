package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The panel is no longer monitoring-only, and the two places that said so are
// the two places that now do the work: the corner of the Tunnels heading, and
// the empty state.
func TestThePanelNoLongerCallsItselfMonitoringOnly(t *testing.T) {
	body := string(dashboardHTML)

	// Comments may still refer to it — the corner is described by what it
	// replaced. What must be gone is anything the reader can see.
	for _, stale := range []string{
		">monitoring only<",
		"Tunnels are created and managed from the CLI",
		"No tunnels configured yet. Create one from the CLI menu",
	} {
		if strings.Contains(body, stale) {
			t.Errorf("the page still shows %q — it can create and manage tunnels now", stale)
		}
	}
	for _, want := range []string{"openAdd()", "restartAllTunnels()"} {
		if !strings.Contains(body, want) {
			t.Errorf("the Tunnels heading has no button wired to %s", want)
		}
	}
}

// The order of the card buttons is the order they were asked for, and it is not
// arbitrary: the three that open a screen come first, then the four that change
// the tunnel. Reordering them silently would move Delete under a thumb that was
// aiming for Restart.
func TestCardButtonsKeepTheirOrder(t *testing.T) {
	card := between(string(dashboardHTML), "function buildCard(t){", "\n}")
	if card == "" {
		t.Fatal("buildCard could not be found")
	}

	want := []string{"openEdit", "openLogs", "openDetails",
		"'start'", "'stop'", "'restart'", "'delete'"}
	at := -1
	for _, w := range want {
		i := strings.Index(card, w)
		if i < 0 {
			t.Fatalf("the card has no button for %s", w)
		}
		if i < at {
			t.Errorf("%s is out of order — want Edit, Logs, Details, then Start, Stop, Restart, Delete", w)
		}
		at = i
	}

	// Delete is the one action on a card that cannot be undone, so it asks
	// first and is marked apart from the other three.
	if !strings.Contains(card, `class="actbtn danger"`) {
		t.Error("Delete is styled like every other action")
	}
	if !strings.Contains(string(dashboardHTML), "action==='delete'&&!confirm(") {
		t.Error("Delete does not ask before removing a tunnel")
	}
}

// Every field the setup form collects has to reach the request, or it is a
// control that appears to work and does nothing.
func TestSetupFormSendsEveryFieldItCollects(t *testing.T) {
	body := string(dashboardHTML)
	submit := between(body, "async function submitAdd(){", "\n}")
	if submit == "" {
		t.Fatal("submitAdd could not be found")
	}

	for id, field := range map[string]string{
		"af-transport": "transport",
		"af-name":      "name",
		"af-port":      "tunnelPort",
		"af-addr":      "serverAddr",
		"af-token":     "token",
		"af-ports":     "ports",
		"af-preset":    "preset",
		"af-ipv6":      "ipv6",
		"af-pp":        "proxyProtocol",
	} {
		if !regexp.MustCompile(`id="` + id + `"`).MatchString(body) {
			t.Errorf("the form has no %s field", id)
		}
		if !strings.Contains(submit, field+":") {
			t.Errorf("%s is collected but never sent as %q", id, field)
		}
		if !strings.Contains(submit, "$('"+id+"')") {
			t.Errorf("%s is in the markup but never read when the form is submitted", id)
		}
	}
	if !strings.Contains(submit, "tune:") {
		t.Error("the Fine Tune drawer is never sent")
	}
	// An untouched drawer must send nothing: a tunnel should not be marked
	// custom because somebody opened the section and looked at it.
	if !strings.Contains(submit, "dataset.dirty==='1'?readTune") {
		t.Error("an untouched Fine Tune drawer is still sent, which would mark the tunnel custom")
	}
}

// The edit form sends only what changed. Everything else would count as a
// change on the server and restart the tunnel for nothing.
func TestEditFormSendsOnlyWhatChanged(t *testing.T) {
	edit := between(string(dashboardHTML), "async function submitEdit(){", "\n}")
	if edit == "" {
		t.Fatal("submitEdit could not be found")
	}
	for _, want := range []string{
		"!==(s.serverHost||'')",
		"!==(s.tunnelPort||'')",
		"!==(s.ports||[]).join(', ')",
		"!==s.transport",
		"!==s.preset",
	} {
		if !strings.Contains(edit, want) {
			t.Errorf("a field is sent without checking it changed (missing %q)", want)
		}
	}
	if !strings.Contains(edit, "Object.keys(body).length===1") {
		t.Error("submitting an untouched form posts an empty edit instead of saying so")
	}
}

// Both dialogs hide rows by class. The layout rules set display, which outranks
// the browser's own [hidden], so a row hidden with the attribute alone would
// stay on screen — a client asked for forwarded ports it does not have.
func TestFormRowsHideByClassNotByAttribute(t *testing.T) {
	body := string(dashboardHTML)
	if !strings.Contains(body, ".hide{display:none!important}") {
		t.Fatal("there is no rule that actually hides a form row")
	}
	for _, id := range []string{"addform", "editform"} {
		if regexp.MustCompile(`id="` + id + `"[^>]*\shidden`).MatchString(body) {
			t.Errorf("%s hides with the attribute, which its own display rule overrides", id)
		}
	}
	for _, call := range []string{
		`document.querySelectorAll('#addform .srv-only')`,
		`document.querySelectorAll('#addform .cli-only')`,
		`document.querySelectorAll('#editform .srv-only')`,
		`document.querySelectorAll('#editform .cli-only')`,
	} {
		if !strings.Contains(body, call) {
			t.Errorf("nothing switches the rows for the other side (missing %s)", call)
		}
	}
}

// The Fine Tune drawer is emptied before either dialog fills it. refreshTune
// deliberately keeps what is already in there — that is what a transport change
// needs — so a drawer left from the last tunnel would carry its numbers, and
// its "custom" mark, into the next one.
func TestFineTuneDrawerIsClearedBetweenTunnels(t *testing.T) {
	body := string(dashboardHTML)
	for _, box := range []string{"af-tune", "ef-tune"} {
		if !strings.Contains(body, "$('"+box+"').innerHTML=''; $('"+box+"').dataset.dirty='0';") {
			t.Errorf("%s is not cleared before the dialog is filled", box)
		}
	}
}

// The transport and preset menus are served, never written into the page: one
// definition in Go, shown by both the CLI and the panel.
func TestMenusComeFromTheServer(t *testing.T) {
	body := string(dashboardHTML)
	if !strings.Contains(body, "fetch('/api/tunnel/options')") {
		t.Error("the panel does not ask the server what the transports are")
	}
	// Both menus have to be built from that answer. A second list written into
	// the page is the failure this guards against: it would go on offering a
	// transport after the CLI dropped it, and never offer one it gained.
	for _, fn := range []string{"function fillFamilies(", "function fillTransports(", "function fillPresets("} {
		built := between(body, fn, "\n}")
		if built == "" {
			t.Fatalf("%s could not be found", fn)
		}
		if !strings.Contains(built, "TOPTS.") {
			t.Errorf("%s builds its options from something other than the server's answer", fn)
		}
	}
}
