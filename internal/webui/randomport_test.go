package webui

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// Random belongs to the tunnel port and nowhere else.
//
// This is the bug report "I made a plain TCP tunnel in the panel and the config
// did not work". The Forwarded ports field had a Random button beside it, and
// the handler was bound to every button labelled Random on the form. What
// Random asks for is a port that is free on the machine the panel runs on —
// which is exactly right for the tunnel port, since that is the port this side
// binds and the other side dials.
//
// For a forwarded port it is the opposite of right. The field's own hint says
// the value goes to the same port on the kharej machine, so a port chosen for
// being free here is, by construction, one nothing is listening on there. The
// button could only ever build a tunnel that comes up, reports a peer, shows
// green, and refuses every connection at the last hop — which is precisely what
// it did.
func TestRandomIsOfferedOnlyForThePortThisSideBinds(t *testing.T) {
	loadPanel()

	raw, err := fs.ReadFile(panelRoot, "views/add.html")
	if err != nil {
		t.Fatalf("add.html: %v", err)
	}
	html := string(raw)

	// Every input paired with a Random button, by the wrapper they share.
	for _, w := range regexp.MustCompile(`(?s)<div class="withb">(.*?)</div>`).
		FindAllStringSubmatch(html, -1) {
		block := w[1]
		if !strings.Contains(strings.ToLower(block), "random") {
			continue
		}
		name := regexp.MustCompile(`name="([^"]+)"`).FindStringSubmatch(block)
		if name == nil {
			t.Errorf("a Random button sits beside no named field:\n%s", block)
			continue
		}
		if name[1] != "tunnelPort" {
			t.Errorf("Random is offered for %q. It asks for a port free on THIS "+
				"machine, which is only meaningful for the port this side binds — "+
				"on a forwarded port it guarantees nothing is listening at the far "+
				"end, and the tunnel comes up carrying nothing.", name[1])
		}
	}
}

// And the handler is bound to that field rather than to every button with the
// same label, so putting one anywhere else cannot silently repeat this.
func TestTheRandomHandlerIsBoundToTheFieldNotTheLabel(t *testing.T) {
	loadPanel()

	raw, err := fs.ReadFile(panelRoot, "js/views/add.js")
	if err != nil {
		t.Fatalf("add.js: %v", err)
	}
	js := string(raw)

	i := strings.Index(js, "api.tunnelSuggest()")
	if i < 0 {
		t.Fatal("nothing on the form asks for a suggested port any more")
	}
	// The binding above the call has to name the field it writes into.
	start := i - 900
	if start < 0 {
		start = 0
	}
	if !strings.Contains(js[start:i], `name="tunnelPort"`) {
		t.Error("the suggested port is written into whatever input happens to be " +
			"beside the button, rather than into the tunnel port — a Random button " +
			"added to any other field would silently start filling it")
	}
}
