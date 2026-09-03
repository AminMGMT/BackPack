package manage

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tunnel/direct"
	"github.com/backpack/backpack/internal/tunnel/l3"
)

// l3EncapForTest builds the encapsulation the engine would, from what the
// wizard wrote — the check that a name or key written here is one the engine
// actually knows.
func l3EncapForTest(cfg config.Config) (l3.Encap, error) {
	return l3.NewEncap(cfg.L3.Encap, cfg.L3.GREKey)
}

// validateDirectForTest puts a decoded config through the engine's own
// validation, exactly as cmd does at startup. It is what turns "the file
// parses" into "the tunnel would actually start".
func validateDirectForTest(cfg config.Config) error {
	d := cfg.Direct
	engineCfg := direct.Config{
		Role:      d.ResolvedRole(),
		Addr:      d.Addr,
		Token:     d.Token,
		Transport: d.Transport,
		Ports:     d.Ports,
		AcceptUDP: d.AcceptUDP,
		Sessions:  d.Sessions,
	}
	return engineCfg.Validate()
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

func TestDirectIranRenderRoundTrips(t *testing.T) {
	spec := directSpec{
		Name: "direct-iran-8443", Side: sideIran, Transport: "stealth",
		Addr: "203.0.113.9:8443", Token: "a-long-token",
		Ports: []string{"443", "8080=80"}, AcceptUDP: true, Sessions: 4,
	}
	cfg := decode(t, spec.render())

	if !cfg.Direct.Enabled() {
		t.Fatal("the rendered config does not report a direct tunnel")
	}
	if cfg.L3.Enabled() {
		t.Fatal("a direct config also reported a layer-3 tunnel")
	}
	if cfg.Server.BindAddr != "" || cfg.Client.RemoteAddr != "" {
		t.Fatal("a direct config populated [server] or [client]")
	}
	if got := cfg.Direct.ResolvedRole(); got != "edge" {
		t.Fatalf("role resolved to %q, want edge", got)
	}
	if cfg.Direct.Addr != spec.Addr || cfg.Direct.Token != spec.Token {
		t.Fatalf("addr/token round trip: %+v", cfg.Direct)
	}
	if cfg.Direct.Transport != "stealth" {
		t.Fatalf("transport = %q, want stealth", cfg.Direct.Transport)
	}
	if strings.Join(cfg.Direct.Ports, ",") != "443,8080=80" {
		t.Fatalf("ports = %v", cfg.Direct.Ports)
	}
	if !cfg.Direct.AcceptUDP {
		t.Fatal("accept_udp did not round trip")
	}
	if cfg.Direct.Sessions != 4 {
		t.Fatalf("sessions = %d, want 4", cfg.Direct.Sessions)
	}
}

// The kharej side holds no port list, and writing one would be a lie about
// where forwarding is configured.
func TestDirectKharejRenderHasNoPorts(t *testing.T) {
	spec := directSpec{
		Name: "direct-kharej-8443", Side: sideKharej, Transport: "wss",
		Addr: "0.0.0.0:8443", Token: "a-long-token",
		ACMEDomain: "tunnel.example.com", ACMEEmail: "you@example.com",
	}
	body := spec.render()
	cfg := decode(t, body)

	if got := cfg.Direct.ResolvedRole(); got != "origin" {
		t.Fatalf("role resolved to %q, want origin", got)
	}
	if len(cfg.Direct.Ports) != 0 {
		t.Fatalf("the kharej side was given ports: %v", cfg.Direct.Ports)
	}
	if strings.Contains(body, "ports") {
		t.Fatalf("the kharej config mentions ports:\n%s", body)
	}
	if cfg.Direct.ACMEDomain != "tunnel.example.com" || cfg.Direct.ACMEEmail != "you@example.com" {
		t.Fatalf("acme round trip: %+v", cfg.Direct)
	}
}

func TestL3RenderRoundTrips(t *testing.T) {
	spec := l3Spec{
		Name: "l3-iran-9000", Side: sideIran, Carrier: "pck",
		Addr: "203.0.113.9:9000", Token: "a-long-token",
		Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1380,
		Ports: []string{"443"}, AcceptUDP: true,
	}
	cfg := decode(t, spec.render())

	if !cfg.L3.Enabled() {
		t.Fatal("the rendered config does not report a layer-3 tunnel")
	}
	if cfg.Direct.Enabled() {
		t.Fatal("an l3 config also reported a direct tunnel")
	}
	if cfg.L3.Mode != "dial" {
		t.Fatalf("mode = %q, want dial", cfg.L3.Mode)
	}
	if cfg.L3.Carrier != "pck" || cfg.L3.MTU != 1380 {
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
	cfg := decode(t, spec.render())

	if cfg.L3.Mode != "listen" {
		t.Fatalf("mode = %q, want listen", cfg.L3.Mode)
	}
	if cfg.L3.SpoofPeerIP != "198.51.100.4" {
		t.Fatalf("spoof_peer_ip = %q", cfg.L3.SpoofPeerIP)
	}
}

// A token with a quote in it must not break the file it is written into.
func TestRenderEscapesQuotes(t *testing.T) {
	spec := directSpec{
		Side: sideIran, Transport: "tcp", Addr: "1.2.3.4:8443",
		Token: `a"quoted"token`, Ports: []string{"443"},
	}
	cfg := decode(t, spec.render())
	if cfg.Direct.Token != `a"quoted"token` {
		t.Fatalf("token = %q, want the original with its quotes", cfg.Direct.Token)
	}
}

// The management screens have to recognise both kinds, or a tunnel that runs
// perfectly is invisible to every menu.
func TestListRecognisesBothKinds(t *testing.T) {
	direct := decode(t, directSpec{
		Side: sideIran, Transport: "ws", Addr: "1.2.3.4:8443",
		Token: "t", Ports: []string{"443"},
	}.render())
	if got := directRole(direct.Direct.ResolvedRole()); got != "iran" {
		t.Fatalf("direct role label = %q, want iran", got)
	}

	l3 := decode(t, l3Spec{
		Side: sideKharej, Carrier: "xdi", Addr: "0.0.0.0:9000", Token: "t",
		Iface: "bp0", LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1400,
	}.render())
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

// The engine must accept what the wizard writes. This is the check that would
// have caught a key renamed on one side only.
func TestRenderedConfigsPassEngineValidation(t *testing.T) {
	iran := decode(t, directSpec{
		Side: sideIran, Transport: "stealth", Addr: "1.2.3.4:8443",
		Token: "a-long-token", Ports: []string{"443", "2053-2060"},
	}.render())
	if err := validateDirectForTest(iran); err != nil {
		t.Fatalf("the engine refused a wizard-written iran config: %v", err)
	}

	kharej := decode(t, directSpec{
		Side: sideKharej, Transport: "stealth", Addr: "0.0.0.0:8443",
		Token: "a-long-token",
	}.render())
	if err := validateDirectForTest(kharej); err != nil {
		t.Fatalf("the engine refused a wizard-written kharej config: %v", err)
	}
}

// GRE is implemented in the engine, so the wizard must be able to reach it —
// it could not, until the encap question was added.
func TestL3EncapRoundTrips(t *testing.T) {
	// Whatever a caller asks for, one encapsulation is written.
	plain := decode(t, l3Spec{
		Side: sideIran, Carrier: "udp", Encap: "ipip", Addr: "1.2.3.4:9000",
		Token: "t", Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
	}.render())
	if plain.L3.Encap != "gre" {
		t.Fatalf("encap = %q, want gre", plain.L3.Encap)
	}
	if plain.L3.GREKey != 0 {
		t.Fatalf("an unkeyed tunnel was given a gre key: %d", plain.L3.GREKey)
	}

	keyed := decode(t, l3Spec{
		Side: sideIran, Carrier: "pck", Encap: "gre", GREKey: 4242, Addr: "1.2.3.4:9000",
		Token: "t", Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1380,
	}.render())
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
	d := decode(t, directSpec{
		Side: sideIran, Transport: "tcp", Addr: "1.2.3.4:8443", Token: "t",
		Ports: []string{"443"}, MaxConnections: 250, BandwidthMbps: 100,
	}.render())
	if d.Direct.MaxConnections != 250 || d.Direct.BandwidthMbps != 100 {
		t.Fatalf("direct limits: %+v", d.Direct)
	}

	l := decode(t, l3Spec{
		Side: sideIran, Carrier: "udp", Encap: "ipip", Addr: "1.2.3.4:9000", Token: "t",
		Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
		Ports: []string{"443"}, MaxConnections: 64, BandwidthMbps: 50,
	}.render())
	if l.L3.MaxConnections != 64 || l.L3.BandwidthMbps != 50 {
		t.Fatalf("l3 limits: %+v", l.L3)
	}

	// An uncapped tunnel must not write the keys at all — a zero in the file
	// reads like a setting of zero.
	plain := directSpec{
		Side: sideIran, Transport: "tcp", Addr: "1.2.3.4:8443", Token: "t",
		Ports: []string{"443"},
	}.render()
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

// A preset must reach the engine as the four numbers behind it, not just as a
// label — a name that tuned nothing would be worse than no preset at all.
func TestPresetRoundTrips(t *testing.T) {
	spec := directSpec{
		Side: sideIran, Transport: "tcp", Addr: "1.2.3.4:8443", Token: "t",
		Ports: []string{"443"},
	}
	findDirectPreset(PresetThroughput).apply(&spec)

	cfg := decode(t, spec.render())
	d := cfg.Direct

	if d.Preset != PresetThroughput {
		t.Fatalf("preset = %q, want %q", d.Preset, PresetThroughput)
	}
	if d.MaxStreamBuffer != 16*1024*1024 {
		t.Fatalf("mux_streambuffer = %d, want 16 MiB", d.MaxStreamBuffer)
	}
	if d.MaxReceiveBuffer != 32*1024*1024 {
		t.Fatalf("mux_recievebuffer = %d, want 32 MiB", d.MaxReceiveBuffer)
	}
	if d.MaxFrameSize != 65535 {
		t.Fatalf("mux_framesize = %d, want 65535", d.MaxFrameSize)
	}
	if d.Sessions != 4 {
		t.Fatalf("sessions = %d, want 4", d.Sessions)
	}

	// A preset must never take away a bigger number the operator set by hand.
	manual := directSpec{
		Side: sideIran, Transport: "tcp", Addr: "1.2.3.4:8443", Token: "t",
		Ports: []string{"443"}, Sessions: 16,
	}
	findDirectPreset(PresetBalance).apply(&manual)
	if manual.Sessions != 16 {
		t.Fatalf("a preset lowered a hand-set session count to %d", manual.Sessions)
	}

	// An unknown name falls back rather than tuning nothing.
	if got := findDirectPreset("nonsense").Name; got != PresetTurbo {
		t.Fatalf("unknown preset resolved to %q, want %q", got, PresetTurbo)
	}
}
