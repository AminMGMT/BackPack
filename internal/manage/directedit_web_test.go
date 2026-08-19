package manage

import (
	"testing"

	"github.com/backpack/backpack/config"
)

// The panel's Edit button must reach a direct tunnel.
//
// It did not. Both settings endpoints went through LoadSpec, which reads
// [server] and [client] and refuses anything else by design — so a direct
// tunnel came back "not a client tunnel" and the button did nothing, which is
// why the only way to build one was the CLI.
func TestThePanelReadsADirectTunnelsSettings(t *testing.T) {
	iran := decode(t, l3Spec{
		Name: "web-iran", Side: sideIran, Carrier: "pck", Encap: "gre",
		Addr: "203.0.113.9:9000", Token: "a-long-token", Iface: "bp0",
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1371,
		Ports: []string{"443", "8080=80"}, AcceptUDP: true,
	}.render()).L3

	set := directSettingsFrom("web-iran", iran)
	if set.Side != "iran" || set.Carrier != "pck" {
		t.Errorf("side/carrier = %q/%q", set.Side, set.Carrier)
	}
	if set.Encap != "GRE + Noise" {
		t.Errorf("encap label = %q — the card must not show an internal name", set.Encap)
	}
	if set.Ports != "443, 8080=80" || !set.AcceptUDP {
		t.Errorf("ports = %q udp = %v", set.Ports, set.AcceptUDP)
	}
	if !set.HoldsPorts {
		t.Error("the Iran side was reported as holding no ports")
	}
	if set.MTU != 1371 || !set.AutoMTU {
		t.Errorf("mtu = %d auto = %v", set.MTU, set.AutoMTU)
	}

	kharej := decode(t, l3Spec{
		Name: "web-kharej", Side: sideKharej, Carrier: "pck", Encap: "gre",
		Addr: "0.0.0.0:9000", Token: "a-long-token", Iface: "bp0",
		LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1371,
	}.render()).L3
	if directSettingsFrom("web-kharej", kharej).HoldsPorts {
		t.Error("the kharej side was reported as holding ports — the form would show a list it has none of")
	}
}

// A field the form did not send must come back unchanged.
//
// A zero meaning "set this to zero" and a zero meaning "the form did not ask"
// are otherwise the same value, and the second one silently wipes settings that
// were never on screen.
func TestAPanelEditLeavesUntouchedFieldsAlone(t *testing.T) {
	before := decode(t, l3Spec{
		Name: "keep", Side: sideIran, Carrier: "spoof", Encap: "gre", GREKey: 7,
		Addr: "203.0.113.9:9000", Token: "a-long-token", Iface: "bp0",
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1371,
		Ports: []string{"443"}, AcceptUDP: true,
		MaxConnections: 50, BandwidthMbps: 100, SockBuf: 8 << 20,
		Spoof: config.SpoofConfig{SpoofPeerIP: "198.51.100.4", SpoofProfile: "tcp"},
	}.render()).L3

	// Only the ports are sent.
	ports := "443, 9090"
	after, err := applyDirectEdit(before, DirectEdit{Ports: &ports})
	if err != nil {
		t.Fatalf("applyDirectEdit: %v", err)
	}

	// And the whole thing has to survive being written back out.
	round := decode(t, directSpecFrom("keep", after).render()).L3

	if len(round.Ports) != 2 || round.Ports[1] != "9090" {
		t.Errorf("the edit did not take: %v", round.Ports)
	}
	for _, c := range []struct {
		key        string
		want, have any
	}{
		{"token", "a-long-token", round.Token},
		{"carrier", "spoof", round.Carrier},
		{"gre_key", uint32(7), round.GREKey},
		{"mtu", 1371, round.MTU},
		{"accept_udp", true, round.AcceptUDP},
		{"max_connections", 50, round.MaxConnections},
		{"bandwidth_mbps", 100, round.BandwidthMbps},
		{"sockbuf", 8 << 20, round.SockBuf},
		{"spoof_peer_ip", "198.51.100.4", round.SpoofPeerIP},
		{"spoof_profile", "tcp", round.SpoofProfile},
	} {
		if c.want != c.have {
			t.Errorf("editing the ports changed %s: was %v, now %v", c.key, c.want, c.have)
		}
	}
}

// The edit must refuse what the wizard refuses.
func TestAPanelEditRefusesBadInput(t *testing.T) {
	base := config.L3Config{Mode: "dial", Carrier: "pck", Encap: "gre", MTU: 1371}

	bad := "not-a-port"
	if _, err := applyDirectEdit(base, DirectEdit{Ports: &bad}); err == nil {
		t.Error("an invalid port entry was accepted")
	}
	tooBig := 70000
	if _, err := applyDirectEdit(base, DirectEdit{MTU: &tooBig}); err == nil {
		t.Error("an impossible MTU was accepted")
	}
	// Zero means automatic and must stay allowed.
	zero := 0
	if _, err := applyDirectEdit(base, DirectEdit{MTU: &zero}); err != nil {
		t.Errorf("mtu 0 (automatic) was refused: %v", err)
	}
}
