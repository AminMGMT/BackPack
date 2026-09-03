package webui

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/manage"
)

// The setup link is gone from the panel, and this is what it leaves behind.
//
// It existed for a second panel on the other server: the operator built one
// end here, copied a link, opened the panel over there and pasted it in. That
// is the two-pass flow the fleet exists to remove, and it is where most of what
// went wrong with pairing came from — two forms, filled in twice, agreeing by
// hand. The panel writes both ends over SSH now.
//
// What must not go with it is the mirroring itself: pushing the far end still
// derives it from the tunnel just written, through exactly the same code.

func TestThePanelNoLongerHandsOutSetupLinks(t *testing.T) {
	loadExperimentalPanel()

	api, err := fs.ReadFile(panelRoot, "js/api.js")
	if err != nil {
		t.Fatalf("cannot read api.js: %v", err)
	}
	if strings.Contains(string(api), "sharelink") {
		t.Error("the panel still calls the share-link endpoint, which is gone — the " +
			"call would 404 and the wizard would offer a link it cannot build")
	}

	add, err := fs.ReadFile(panelRoot, "js/views/add.js")
	if err != nil {
		t.Fatalf("cannot read add.js: %v", err)
	}
	src := string(add)
	for _, gone := range []string{"shareLinkDecode", "paintHandoff", "applyPastedLink"} {
		if strings.Contains(src, gone) {
			t.Errorf("add.js still has %s, so the second-pass path is still on screen", gone)
		}
	}
	// And it must refuse rather than build half a tunnel.
	if !strings.Contains(src, "noFleet") {
		t.Error("the wizard no longer has a state for having no managed server, so with " +
			"an empty fleet it would build this end and leave the other undone")
	}
}

// The mirroring is what the push depends on, so it stays, and stays exercised.
func TestTheMirrorStillDerivesTheFarEnd(t *testing.T) {
	link, err := manage.ShareLink{
		V: 1, Kind: "reverse", From: "iran", Name: "fr-relay",
		Tr: "tcpmux", Host: "203.0.113.9", Port: "8443", Tok: "s3cret",
	}.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	parsed, err := manage.DecodeShareLink(link)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	form := manage.MirrorForPeer(parsed)
	if form.Kind != "reverse" {
		t.Errorf("the mirror changed the kind to %q", form.Kind)
	}
	t2 := form.ToNewTunnel()
	if t2.Role != "client" {
		t.Errorf("the far end of a server is %q, not a client", t2.Role)
	}
	if t2.Token != "s3cret" {
		t.Error("the token did not survive the mirror, so the two ends would not agree")
	}
	if t2.Transport != "tcpmux" {
		t.Errorf("the transport changed to %q", t2.Transport)
	}
}
