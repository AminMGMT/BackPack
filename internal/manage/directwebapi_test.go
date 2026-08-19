package manage

import (
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/tunnel/l3"
)

// The panel and the wizard must write the same file from the same answers.
// Two ways in that produce different configs is two products.
func TestThePanelFormBuildsAWorkingDirectConfig(t *testing.T) {
	spec, err := NewDirectTunnel{
		Side: "iran", Carrier: "pck", Name: "panel-iran",
		Token: "a-long-token", PeerAddr: "203.0.113.9", TunnelPort: "9000",
		Ports: "443,8080=80", AcceptUDP: true, Preset: PresetTurbo,
	}.spec()
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	cfg := decode(t, spec.render())

	if cfg.L3.Mode != "dial" {
		t.Errorf("the Iran side does not dial: %q", cfg.L3.Mode)
	}
	if cfg.L3.Addr != "203.0.113.9:9000" {
		t.Errorf("addr = %q", cfg.L3.Addr)
	}
	// Always Backpack's own GRE. The panel offers no choice, exactly as the
	// wizard offers none.
	if cfg.L3.Encap != "gre" {
		t.Errorf("encap = %q, want gre", cfg.L3.Encap)
	}
	if len(cfg.L3.Ports) != 2 || !cfg.L3.AcceptUDP {
		t.Errorf("ports round trip: %v udp=%v", cfg.L3.Ports, cfg.L3.AcceptUDP)
	}
	if cfg.L3.Preset != PresetTurbo || cfg.L3.TxQueueLen == 0 {
		t.Errorf("preset did not apply: %q queue=%d", cfg.L3.Preset, cfg.L3.TxQueueLen)
	}

	engine := l3.Config{
		Mode: cfg.L3.Mode, Addr: cfg.L3.Addr, Token: cfg.L3.Token,
		Carrier: cfg.L3.Carrier, Encap: cfg.L3.Encap, Iface: cfg.L3.Iface,
		LocalIP: cfg.L3.LocalIP, PeerIP: cfg.L3.PeerIP, MTU: cfg.L3.MTU,
		Ports: cfg.L3.Ports, TxQueueLen: cfg.L3.TxQueueLen, Qdisc: cfg.L3.Qdisc,
	}
	if err := engine.Validate(); err != nil {
		t.Fatalf("the engine refused a panel-written config: %v", err)
	}
}

// The kharej side listens, needs no address to dial and no port list.
func TestThePanelKharejSideListens(t *testing.T) {
	spec, err := NewDirectTunnel{
		Side: "kharej", Carrier: "pck", Name: "panel-kharej",
		Token: "a-long-token", TunnelPort: "9000",
	}.spec()
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	cfg := decode(t, spec.render())

	if cfg.L3.Mode != "listen" {
		t.Errorf("the kharej side does not listen: %q", cfg.L3.Mode)
	}
	if !strings.HasPrefix(cfg.L3.Addr, "0.0.0.0:") {
		t.Errorf("addr = %q, want a bind address", cfg.L3.Addr)
	}
	if len(cfg.L3.Ports) != 0 {
		t.Errorf("the kharej side was given ports: %v", cfg.L3.Ports)
	}
}

// Everything the wizard refuses, the panel must refuse. A form that accepts
// what the wizard rejects is a second way to build a broken tunnel.
func TestThePanelRefusesWhatTheWizardWould(t *testing.T) {
	base := func() NewDirectTunnel {
		return NewDirectTunnel{
			Side: "iran", Carrier: "pck", Name: "n", Token: "t",
			PeerAddr: "1.2.3.4", TunnelPort: "9000", Ports: "443",
		}
	}
	for name, mutate := range map[string]func(*NewDirectTunnel){
		"no side":            func(n *NewDirectTunnel) { n.Side = "" },
		"unknown side":       func(n *NewDirectTunnel) { n.Side = "server" },
		"unknown carrier":    func(n *NewDirectTunnel) { n.Carrier = "kcp" },
		"xdi is not offered": func(n *NewDirectTunnel) { n.Carrier = "xdi" },
		"no token":           func(n *NewDirectTunnel) { n.Token = "  " },
		"bad port":           func(n *NewDirectTunnel) { n.TunnelPort = "70000" },
		"no peer address":    func(n *NewDirectTunnel) { n.PeerAddr = "" },
		"no ports on iran":   func(n *NewDirectTunnel) { n.Ports = "" },
		"bad port spec":      func(n *NewDirectTunnel) { n.Ports = "not-a-port" },
		"bad name":           func(n *NewDirectTunnel) { n.Name = "has spaces/" },
	} {
		n := base()
		mutate(&n)
		if _, err := n.spec(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The spoof carrier cannot learn where its peer is on the listening side.
func TestThePanelDemandsTheSpoofPeer(t *testing.T) {
	n := NewDirectTunnel{Side: "kharej", Carrier: "spoof", Name: "s", Token: "t", TunnelPort: "9000"}
	if _, err := n.spec(); err == nil {
		t.Fatal("a spoof listener without the peer's real IP was accepted")
	}
	n.SpoofPeerIP = "203.0.113.9"
	spec, err := n.spec()
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if spec.Spoof.SpoofPeerIP != "203.0.113.9" {
		t.Fatalf("spoof_peer_ip = %q", spec.Spoof.SpoofPeerIP)
	}
}

// The panel's carrier list must be the wizard's list, in the wizard's order.
func TestThePanelOffersTheSameCarriersAsTheWizard(t *testing.T) {
	var got []string
	for _, c := range DirectCarriers() {
		got = append(got, c["value"])
	}
	if strings.Join(got, ",") != "pck,udp,spoof" {
		t.Fatalf("panel carriers = %v, want pck, udp, spoof in that order", got)
	}
	for _, c := range DirectCarriers() {
		if c["desc"] == "" || c["label"] == "" {
			t.Errorf("carrier %q is offered with no label or description", c["value"])
		}
	}
}

// Only the listening side is offered a token, for the same reason the wizard is
// asymmetric: two accepted defaults means two different tokens, and a
// mismatched token is answered with silence.
func TestOnlyTheKharejSideIsOfferedAToken(t *testing.T) {
	if _, ok := SuggestDirectDefaults("kharej")["token"]; !ok {
		t.Error("the kharej side is not offered a token")
	}
	// The Iran side's token is stripped by the handler, but the subnet and
	// interface it needs must still be there.
	d := SuggestDirectDefaults("iran")
	if d["localIp"] == "" || d["peerIp"] == "" || d["iface"] == "" {
		t.Errorf("the Iran side was given no addressing: %v", d)
	}
}
