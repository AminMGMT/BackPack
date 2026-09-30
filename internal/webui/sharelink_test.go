package webui

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/manage"
)

// Add tunnel builds this server's end and hands over the setup link.
//
// For a while the wizard only built tunnels across a managed server, writing
// both ends over SSH, and refused outright without one. Servers is out of the
// panel now, so the wizard is the CLI's again — Setup Iran or Setup Kharej on
// the machine in front of you — and the other end is built from the link, the
// one line the menu prints too. What must not come back is the old second
// pass: a second panel on the other server with a paste box, filled in twice.
func TestAddTunnelBuildsThisEndAndHandsOverTheLink(t *testing.T) {
	loadPanel()

	api, err := fs.ReadFile(panelRoot, "js/api.js")
	if err != nil {
		t.Fatalf("cannot read api.js: %v", err)
	}
	if strings.Contains(string(api), "sharelink") {
		t.Error("the panel calls the old share-link endpoint, which is gone")
	}

	add, err := fs.ReadFile(panelRoot, "js/views/add.js")
	if err != nil {
		t.Fatalf("cannot read add.js: %v", err)
	}
	src := string(add)
	for _, gone := range []string{"shareLinkDecode", "paintHandoff", "applyPastedLink"} {
		if strings.Contains(src, gone) {
			t.Errorf("add.js still has %s, the second-pass paste flow", gone)
		}
	}
	if strings.Contains(src, "api.nodes(") || strings.Contains(src, "noFleet") {
		t.Error("the wizard still waits on the fleet, which is not part of the panel any more")
	}
	if !strings.Contains(src, "api.tunnelLink(") || !strings.Contains(src, "setupLinkHTML(") {
		t.Error("the wizard builds this end and does not hand over the setup link, so the " +
			"other end has no way to be built from here")
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
