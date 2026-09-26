package manage

import (
	"strings"
	"testing"
)

// Everything a direct tunnel's two ends must agree on reaches the kharej's
// config from the Iran server's link — including the settings the link used to
// drop: a GRE key, an error-correction pair other than the recommended one, the
// MTU and the segment cap. Any one of those differing is a tunnel that comes
// up and carries nothing.
func TestTheKharejGetsEveryPairedSettingFromTheLink(t *testing.T) {
	off := false
	sent := ShareLink{
		Kind: "direct", From: "iran", Name: "almani",
		Tok: strings.Repeat("k", 64), Tr: "pck", Encap: "gre", Port: "5376",
		LocalIP: "10.10.2.1/30", PeerIP: "10.10.2.2",
		FECData: 12, FECParity: 5, MTU: 1380,
		GREKey: 7, L3MSS: 1300, AutoMTU: &off,
	}
	enc, err := sent.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeShareLink(enc)
	if err != nil {
		t.Fatal(err)
	}
	form := MirrorForPeer(got)
	if form.Side != "kharej" {
		t.Fatalf("the link is for side %q, want kharej", form.Side)
	}
	spec, err := form.ToNewDirectTunnel().spec()
	if err != nil {
		t.Fatal(err)
	}
	body := spec.render()
	for _, want := range []string{
		`mode         = "listen"`,
		`local_ip     = "10.10.2.2"`,
		`peer_ip      = "10.10.2.1"`,
		"gre_key      = 7",
		"mtu          = 1380",
		"12", "5", // the exact pair
		"1300", // the segment cap
		"auto_mtu",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the kharej config is missing %q:\n%s", want, body)
		}
	}
	if spec.FECData != 12 || spec.FECParity != 5 {
		t.Fatalf("FEC %d/%d, want the Iran server's 12/5", spec.FECData, spec.FECParity)
	}
	if spec.AutoMTU == nil || *spec.AutoMTU {
		t.Fatal("auto_mtu off on the Iran server did not reach the kharej")
	}
}
