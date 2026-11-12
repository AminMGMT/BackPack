package manage

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tunnel/l3"
)

// l3EncapForTest builds the encapsulation the engine would, from what the
// wizard wrote — the check that a name or key written here is one the engine
// actually knows.
func l3EncapForTest(cfg config.Config) (l3.Encap, error) {
	return l3.NewEncap(cfg.L3.Encap, cfg.L3.GREKey)
}

// What the wizard writes has to come back as what it meant. These decode the
// rendered file the same way the engine does, so a change to either side that
// breaks the round trip fails here rather than on somebody's server.

func decode(t *testing.T, body string) config.Config {
	t.Helper()
	var cfg config.Config
	if _, err := toml.Decode(body, &cfg); err != nil {
		t.Fatalf("the rendered config does not parse: %v\n---\n%s", err, body)
	}
	return cfg
}

func TestL3RenderRoundTrips(t *testing.T) {
	spec := l3Spec{
		Name: "l3-iran-9000", Side: sideIran, Carrier: "xdi",
		Addr: "203.0.113.9:9000", Token: "a-long-token",
		Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1380,
		Ports: []string{"443"}, AcceptUDP: true,
	}
	cfg := decode(t, spec.Render())

	if !cfg.L3.Enabled() {
		t.Fatal("the rendered config does not report a layer-3 tunnel")
	}
	if cfg.Direct.Present() {
		t.Fatal("an l3 config also reported a [direct] table")
	}
	if cfg.L3.Mode != "dial" {
		t.Fatalf("mode = %q, want dial", cfg.L3.Mode)
	}
	if cfg.L3.Carrier != "xdi" || cfg.L3.MTU != 1380 {
		t.Fatalf("carrier/mtu round trip: %+v", cfg.L3)
	}
	if cfg.L3.LocalIP != "10.10.0.1/30" || cfg.L3.PeerIP != "10.10.0.2" {
		t.Fatalf("addresses round trip: %+v", cfg.L3)
	}
	if len(cfg.L3.Ports) != 1 || !cfg.L3.AcceptUDP {
		t.Fatalf("ports round trip: %+v", cfg.L3)
	}
}

func TestL3KharejRendersListen(t *testing.T) {
	spec := l3Spec{
		Name: "l3-kharej-9000", Side: sideKharej, Carrier: "spoof",
		Addr: "0.0.0.0:9000", Token: "a-long-token",
		Iface: "bp0", LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1400,
		Spoof: config.SpoofConfig{SpoofPeerIP: "198.51.100.4"},
	}
	cfg := decode(t, spec.Render())

	if cfg.L3.Mode != "listen" {
		t.Fatalf("mode = %q, want listen", cfg.L3.Mode)
	}
	if cfg.L3.SpoofPeerIP != "198.51.100.4" {
		t.Fatalf("spoof_peer_ip = %q", cfg.L3.SpoofPeerIP)
	}
}

// A token with a quote in it must not break the file it is written into.
func TestRenderEscapesQuotes(t *testing.T) {
	spec := l3Spec{
		Side: sideIran, Carrier: "xdi", Addr: "1.2.3.4:9000",
		Token: `a"quoted"token`, Ports: []string{"443"},
		Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
	}
	cfg := decode(t, spec.Render())
	if cfg.L3.Token != `a"quoted"token` {
		t.Fatalf("token = %q, want the original with its quotes", cfg.L3.Token)
	}
}

// The management screens have to recognise both kinds — the [direct] one too,
// though it can no longer run, so that it is listed and can be deleted rather
// than vanishing from every menu while its service is still installed.
func TestListRecognisesBothKinds(t *testing.T) {
	l3 := decode(t, l3Spec{
		Side: sideKharej, Carrier: "xdi", Addr: "0.0.0.0:9000", Token: "t",
		Iface: "bp0", LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1400,
	}.Render())
	if got := l3Role(l3.L3.Mode); got != "kharej" {
		t.Fatalf("l3 role label = %q, want kharej", got)
	}

	if !IsDirectKind(Tunnel{Transport: "direct/ws"}) || !IsDirectKind(Tunnel{Transport: "l3/xdi"}) {
		t.Fatal("IsDirectKind did not recognise a direct tunnel")
	}
	if IsDirectKind(Tunnel{Transport: "tcpmux"}) {
		t.Fatal("IsDirectKind claimed a reverse tunnel")
	}
}

// GRE is implemented in the engine, so the wizard must be able to reach it —
// it could not, until the encap question was added.
func TestL3EncapRoundTrips(t *testing.T) {
	// Whatever a caller asks for, one encapsulation is written.
	plain := decode(t, l3Spec{
		Side: sideIran, Carrier: "xdi", Encap: "ipip", Addr: "1.2.3.4:9000",
		Token: "t", Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
	}.Render())
	if plain.L3.Encap != "gre" {
		t.Fatalf("encap = %q, want gre", plain.L3.Encap)
	}
	if plain.L3.GREKey != 0 {
		t.Fatalf("an unkeyed tunnel was given a gre key: %d", plain.L3.GREKey)
	}

	keyed := decode(t, l3Spec{
		Side: sideIran, Carrier: "xdi", Encap: "gre", GREKey: 4242, Addr: "1.2.3.4:9000",
		Token: "t", Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1380,
	}.Render())
	if keyed.L3.Encap != "gre" || keyed.L3.GREKey != 4242 {
		t.Fatalf("gre round trip: encap=%q key=%d", keyed.L3.Encap, keyed.L3.GREKey)
	}

	// The engine has to accept what the wizard wrote.
	if _, err := l3EncapForTest(keyed); err != nil {
		t.Fatalf("the engine refused a wizard-written gre config: %v", err)
	}

	// The label says "+ Noise" and the config key does not. Both halves matter:
	// the screens have to distinguish this from the kernel's own GRE — bare IP
	// protocol 47, unencrypted — while the file keeps the plain value both ends
	// compare, which cannot change without breaking every tunnel already built.
	if got := l3EncapLabel(keyed.L3); got != "GRE + Noise (key 4242)" {
		t.Fatalf("label = %q", got)
	}
	if got := l3EncapLabel(plain.L3); got != "GRE + Noise" {
		t.Fatalf("label = %q", got)
	}
	if keyed.L3.Encap != "gre" || plain.L3.Encap != "gre" {
		t.Fatal("the display name leaked into the config value, which both ends compare")
	}
}

// The caps have to survive the round trip and reach the engine, or the wizard
// would collect a number that quietly does nothing.
func TestLimitsRoundTrip(t *testing.T) {
	l := decode(t, l3Spec{
		Side: sideIran, Carrier: "xdi", Encap: "ipip", Addr: "1.2.3.4:9000", Token: "t",
		Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
		Ports: []string{"443"}, MaxConnections: 64, BandwidthMbps: 50,
	}.Render())
	if l.L3.MaxConnections != 64 || l.L3.BandwidthMbps != 50 {
		t.Fatalf("l3 limits: %+v", l.L3)
	}

	// An uncapped tunnel must not write the keys at all — a zero in the file
	// reads like a setting of zero.
	plain := l3Spec{
		Side: sideIran, Carrier: "xdi", Addr: "1.2.3.4:9000", Token: "t",
		Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
		Ports: []string{"443"},
	}.Render()
	if strings.Contains(plain, "max_connections") || strings.Contains(plain, "bandwidth_mbps") {
		t.Fatalf("an uncapped tunnel wrote limit keys:\n%s", plain)
	}

	if got := limitsLabel(0, 0); got != "unlimited, unlimited" {
		t.Fatalf("label = %q", got)
	}
	if got := limitsLabel(250, 100); got != "250 connections, 100 Mbit/s" {
		t.Fatalf("label = %q", got)
	}
}
