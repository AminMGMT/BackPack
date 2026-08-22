package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The panel's Edit button has to open the editor that matches the tunnel.
//
// This is the regression test for "you go to edit a direct tunnel and the
// reverse editor comes up". The server had answered correctly all along —
// handleTunnelSettings returns {"kind":"direct","direct":{…}} for a direct
// tunnel — but the page never looked at kind. It fell straight through into the
// reverse form, which reads s.role and s.transport; a direct tunnel has
// neither, so every field came up blank, the transport picker offered the
// reverse transports, and saving posted a reverse edit with nothing in it.
func TestTheEditDrawerBranchesOnTunnelKind(t *testing.T) {
	page := string(dashboardHTML)

	if !strings.Contains(page, "s.kind==='direct'") {
		t.Error("openEdit does not look at the kind the server sent, so a direct " +
			"tunnel opens the reverse form")
	}
	if !strings.Contains(page, "editOrig.kind==='direct'") {
		t.Error("submitEdit does not branch on kind, so saving a direct tunnel posts " +
			"a reverse edit")
	}
	if !strings.Contains(page, `id="editdform"`) {
		t.Error("there is no direct edit form for the branch to open")
	}
}

// Saving must send the direct form's own body, or the server receives an empty
// DirectEdit and restarts the tunnel having changed nothing.
func TestTheDirectEditPostsItsOwnFields(t *testing.T) {
	page := string(dashboardHTML)

	start := strings.Index(page, "async function submitDirectEdit()")
	if start < 0 {
		t.Fatal("submitDirectEdit is missing")
	}
	body := page[start:]
	if end := strings.Index(body, "\n// ---- start / stop"); end > 0 {
		body = body[:end]
	}

	// Every field DirectEdit accepts that the form offers.
	for _, key := range []string{
		"direct.ports", "direct.acceptUdp", "direct.preset",
		"direct.mtu", "direct.autoMtu", "direct.maxConnections", "direct.bandwidthMbps",
	} {
		if !strings.Contains(body, key) {
			t.Errorf("submitDirectEdit never sets %s, so that field cannot be changed", key)
		}
	}
	if !strings.Contains(body, "direct:direct") {
		t.Error("the request body does not carry the direct object the server reads")
	}
	if !strings.Contains(body, "/api/tunnel/edit") {
		t.Error("submitDirectEdit posts somewhere other than the edit endpoint")
	}
}

// The kharej side of a direct tunnel keeps no port list — every target arrives
// on the stream that asks for it — so the port fields must not be offered
// there.
func TestThePortFieldsFollowWhichSideHoldsThem(t *testing.T) {
	page := string(dashboardHTML)

	start := strings.Index(page, "async function openDirectEdit(")
	if start < 0 {
		t.Fatal("openDirectEdit is missing")
	}
	body := page[start : start+2500]

	if !strings.Contains(body, "d.holdsPorts") {
		t.Error("openDirectEdit ignores holdsPorts, so the kharej side is shown a " +
			"port list it does not have")
	}
	if !strings.Contains(body, "edf-ports") {
		t.Error("openDirectEdit does not fill the port field")
	}
}

// Every id the direct edit code touches has to exist in the markup, or the
// drawer throws on a null and shows nothing at all.
func TestEveryDirectEditFieldExists(t *testing.T) {
	page := string(dashboardHTML)

	ids := regexp.MustCompile(`\$\('(edf-[a-z]+)'\)`).FindAllStringSubmatch(page, -1)
	if len(ids) == 0 {
		t.Fatal("the direct edit code references no fields at all")
	}
	seen := map[string]bool{}
	for _, m := range ids {
		id := m[1]
		if seen[id] {
			continue
		}
		seen[id] = true
		if !strings.Contains(page, `id="`+id+`"`) {
			t.Errorf("the code reads %s but no element has that id — the drawer would "+
				"throw before rendering", id)
		}
	}
}
