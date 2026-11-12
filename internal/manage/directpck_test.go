package manage

import (
	"regexp"
	"strings"
	"testing"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/tunnel/l3"
	"github.com/backpack/backpack/internal/tunnel/paqet"
)

// PCK since v2.0.0 is a port-forwarding transport with no TUN device. These
// tests hold the setup, the file, the edit and the setup link to that.

func pckIranSpec() l3Spec {
	return l3Spec{
		Name: "pck-iran-9000", Side: sideIran, Carrier: "pck",
		Addr: "203.0.113.9:9000", Token: "a-long-token-for-pck",
		Preset: PresetTurbo, Ports: []string{"443", "2053=8443"}, AcceptUDP: true,
	}
}

// What a new PCK tunnel's file holds is what the engine reads, and nothing from
// the TUN carriers: no interface, no tunnel addresses, no MTU, no queue, no
// error correction, no encapsulation.
func TestANewPckFileHoldsOnlyWhatTheEngineReads(t *testing.T) {
	for _, spec := range []l3Spec{pckIranSpec(), {
		Name: "pck-kharej-9000", Side: sideKharej, Carrier: "pck",
		Addr: "0.0.0.0:9000", Token: "a-long-token-for-pck", Preset: PresetBalance,
	}} {
		body := spec.Render()
		keys := l3KeySet(t, body)
		for _, dead := range []string{"iface", "local_ip", "peer_ip", "mtu", "txqueuelen", "qdisc", "auto_mtu",
			"mss_clamp", "fec_data", "fec_parity", "encap", "gre_key", "sockbuf"} {
			if keys[dead] {
				t.Errorf("%s: a new PCK file has %q, which the engine does not read:\n%s", spec.Side, dead, body)
			}
		}
		for _, want := range []string{"mode", "addr", "token", "carrier", "preset"} {
			if !keys[want] {
				t.Errorf("%s: the file has no %q:\n%s", spec.Side, want, body)
			}
		}
		if spec.Side == sideKharej && (keys["ports"] || keys["accept_udp"]) {
			t.Errorf("the kharej side's file lists ports it never serves:\n%s", body)
		}
		if spec.Side == sideIran && (!keys["ports"] || !keys["accept_udp"]) {
			t.Errorf("the Iran side's file has no ports:\n%s", body)
		}
		if !strings.Contains(body, "# The token is the encryption key") || !strings.Contains(body, "no network interface") {
			t.Errorf("the file does not explain itself:\n%s", body)
		}
		cfg := decode(t, body)
		engine, err := l3.ConfigFromTable(cfg.L3)
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.Validate(); err != nil {
			t.Errorf("%s: the engine refuses a file the setup wrote: %v\n%s", spec.Side, err, body)
		}
	}
}

// The pck_* values are written where set and only there, and come back.
func TestPckFineTuningRoundTrips(t *testing.T) {
	spec := pckIranSpec()
	if strings.Contains(spec.Render(), "pck_conns") {
		t.Fatal("an untuned file names pck_conns")
	}
	spec.PckConns, spec.PckKCPMode, spec.PckKCPMTU = 3, "fast3", 1300
	spec.PckKCPSndWnd, spec.PckKCPRcvWnd, spec.PckKCPBlock = 2048, 4096, "aes-128-gcm"
	cfg := decode(t, spec.Render()).L3
	if cfg.PckConns != 3 || cfg.PckKCPMode != "fast3" || cfg.PckKCPMTU != 1300 ||
		cfg.PckKCPSndWnd != 2048 || cfg.PckKCPRcvWnd != 4096 || cfg.PckKCPBlock != "aes-128-gcm" {
		t.Fatalf("the pck_* keys did not round trip: %+v", cfg)
	}
	back := directSpecFrom("x", cfg)
	if back.PckConns != 3 || back.PckKCPBlock != "aes-128-gcm" || back.Render() != spec.Render() {
		t.Fatalf("an edit re-renders the file differently:\n%s\n---\n%s", spec.Render(), back.Render())
	}
}

// The panel, a node and a setup link all build a PCK tunnel through spec().
func TestThePanelBuildsAPckTunnel(t *testing.T) {
	spec, err := NewDirectTunnel{
		Side: "iran", Carrier: "pck", PeerAddr: "203.0.113.9", TunnelPort: "9000",
		Ports: "443, 2053=8443", AcceptUDP: true, Preset: "aggressive",
		PckConns: 3, PckKCPMode: "FAST3", PckKCPBlock: "aes-128-gcm",
		// The TUN's own answers are dropped, not refused: a link from an Iran
		// server that has not been updated carries them.
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1380, FEC: true, GREKey: 7,
	}.spec()
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "pck-iran-9000" {
		t.Errorf("default name %q", spec.Name)
	}
	if len(spec.Token) != 64 {
		t.Errorf("the Iran side did not make the key: %q", spec.Token)
	}
	body := spec.Render()
	keys := l3KeySet(t, body)
	for _, dead := range []string{"local_ip", "peer_ip", "mtu", "fec_data", "fec_parity", "gre_key", "iface"} {
		if keys[dead] {
			t.Errorf("a TUN key reached a PCK file: %s\n%s", dead, body)
		}
	}
	cfg := decode(t, body).L3
	if cfg.Preset != "aggressive" || cfg.PckConns != 3 || cfg.PckKCPMode != "fast3" || cfg.PckKCPBlock != "aes-128-gcm" {
		t.Errorf("settings did not arrive: %+v", cfg)
	}
}

func TestThePanelRefusesWhatPckCannotRun(t *testing.T) {
	base := func() NewDirectTunnel {
		return NewDirectTunnel{Side: "iran", Carrier: "pck", PeerAddr: "1.2.3.4", TunnelPort: "9000", Ports: "443"}
	}
	for name, c := range map[string]struct {
		mutate func(*NewDirectTunnel)
		want   string
	}{
		"unknown preset": {func(n *NewDirectTunnel) { n.Preset = "warp" }, "balance, turbo, aggressive"},
		"bad mode":       {func(n *NewDirectTunnel) { n.PckKCPMode = "warp" }, "pck_kcp_mode"},
		"too many":       {func(n *NewDirectTunnel) { n.PckConns = 99 }, "pck_conns"},
		"big packets":    {func(n *NewDirectTunnel) { n.PckKCPMTU = 1500 }, "pck_kcp_mtu"},
		"bad cipher":     {func(n *NewDirectTunnel) { n.PckKCPBlock = "rot13" }, "pck_kcp_block"},
		"no ports":       {func(n *NewDirectTunnel) { n.Ports = "" }, "forwarded port"},
		"ipv6 peer":      {func(n *NewDirectTunnel) { n.PeerAddr = "2001:db8::1" }, "IPv4"},
		"no peer":        {func(n *NewDirectTunnel) { n.PeerAddr = "" }, "address"},
	} {
		n := base()
		c.mutate(&n)
		if _, err := n.spec(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error mentioning %q", name, err, c.want)
		}
	}
	// The kharej cannot invent the key: it must be the Iran server's.
	if _, err := (NewDirectTunnel{Side: "kharej", Carrier: "pck", TunnelPort: "9000"}).spec(); err == nil ||
		!strings.Contains(err.Error(), "token") {
		t.Errorf("a kharej without the key: %v", err)
	}
}

// Presets map to engine parameters, and the three screens show them.
func TestThePresetsDescribeThePckEngine(t *testing.T) {
	for _, p := range DirectPresets() {
		want, _ := paqet.PresetParams(p["value"])
		if p["pck"] != want.Resolve(p["value"]).Summary() || p["pckConns"] == "" || p["pckMode"] == "" {
			t.Errorf("%s: the preset does not describe what it sets in the engine: %v", p["value"], p)
		}
	}
	opts := pckPresetOptions()
	if len(opts) != 3 || opts[0].Title != "Turbo" || !strings.Contains(opts[0].Desc, "2 sessions, fast2, 1024-packet windows") {
		t.Errorf("the wizard's preset question: %+v", opts)
	}
	if got := PckModes(); strings.Join(got, ",") != "normal,fast,fast2,fast3" {
		t.Errorf("modes %v", got)
	}
	if bl := PckBlocks(); bl[0] != "aes" || len(bl) != len(paqet.Blocks()) {
		t.Errorf("blocks %v", bl)
	}
}

// Setup → Direct → PCK asks what every other transport asks, minus what PCK no
// longer needs, and never a TUN question.
func TestThePckWizardAsksNothingAboutATun(t *testing.T) {
	// Iran: kharej address, tunnel port, ports, UDP, name, preset, fine-tune,
	// restart schedule, create — and the final Enter. Accepting every default
	// but the address and the ports.
	restore := tui.SetInput(strings.NewReader("203.0.113.9\n\n9443,2053=8443\ny\n\n1\nn\n0\nn\n"))
	var out string
	func() {
		defer restore()
		out = capture(t, func() { setupPckForTest(sideIran) })
	}()
	for _, ask := range []string{"Kharej IP Or Domain", "Tunnel Port", "Forwarded Ports", "Carry UDP", "Tunnel Name",
		"How Should The Tunnel Be Tuned?", "Fine-Tune", "Create This Tunnel"} {
		if !strings.Contains(out, ask) {
			t.Errorf("the Iran wizard never asked %q:\n%s", ask, out)
		}
	}
	for _, never := range []string{"Interface Name", "Tunnel Address", "MTU", "FEC", "Error Correction", "GRE Key",
		"Security Token", "Blank For TUN", "TCP MSS"} {
		if strings.Contains(out, never) {
			t.Errorf("the PCK wizard asked %q, a question for the TUN carriers:\n%s", never, out)
		}
	}
	if !strings.Contains(out, "2 sessions, fast2, 1024-packet windows") {
		t.Errorf("the presets are not described by what they set:\n%s", out)
	}
	if !strings.Contains(out, "backpack://") {
		t.Errorf("the Iran summary has no setup link:\n%s", out)
	}
}

// The wizard stops before asking anything on a build that cannot capture.
// Run on every machine through the seam rather than the constant.
func TestThePckKharejManualAsksForTheKeyAndNothingAboutATun(t *testing.T) {
	restore := tui.SetInput(strings.NewReader("\nmy-kharej\nthe-iran-token\n\nn\nn\n"))
	var out string
	func() {
		defer restore()
		out = capture(t, func() { setupPckForTest(sideKharej) })
	}()
	for _, want := range []string{"Tunnel Port", "Security Token (From The Iran Server)", "How Should The Tunnel Be Tuned?"} {
		if !strings.Contains(out, want) {
			t.Errorf("the kharej wizard never asked %q:\n%s", want, out)
		}
	}
	for _, never := range []string{"Tunnel Address", "MTU", "FEC", "Interface Name", "Forwarded Ports"} {
		if strings.Contains(out, never) {
			t.Errorf("the PCK kharej wizard asked %q:\n%s", never, out)
		}
	}
}

// An edit of a migrated file re-renders it as a PCK file: the TUN keys go, what
// the engine reads stays, and the fine-tuning the form sends is kept.
func TestEditingAMigratedPckFileDropsTheDeadKeysAndKeepsTheLiveOnes(t *testing.T) {
	old := `[l3]
mode = "dial"
addr = "203.0.113.9:9000"
token = "a-long-token-for-pck"
carrier = "pck"
encap = "gre"
iface = "bp1"
local_ip = "10.10.1.1/30"
peer_ip = "10.10.1.2"
mtu = 1400
preset = "turbo"
txqueuelen = 4096
qdisc = "fq_codel"
sockbuf = 8388608
fec_data = 10
fec_parity = 3
pck_interface = "eth0"
ports = ["443"]
accept_udp = true
`
	ports := "443, 2053"
	conns := 3
	body, err := editedDirectBody("old", old, DirectEdit{Ports: &ports, PckConns: &conns})
	if err != nil {
		t.Fatal(err)
	}
	keys := l3KeySet(t, body)
	for _, dead := range []string{"iface", "local_ip", "peer_ip", "mtu", "txqueuelen", "qdisc", "fec_data", "fec_parity", "encap"} {
		if keys[dead] {
			t.Errorf("an edited PCK file keeps %q:\n%s", dead, body)
		}
	}
	l := decode(t, body).L3
	if l.PckConns != 3 || strings.Join(l.Ports, ",") != "443,2053" || l.PckInterface != "eth0" ||
		l.SockBuf != 8388608 || l.Token != "a-long-token-for-pck" || !l.AcceptUDP {
		t.Errorf("a live setting was lost: %+v\n%s", l, body)
	}
	// And a no-op edit of a file the setup wrote is the same file.
	fresh := pckIranSpec().Render()
	if same, err := editedDirectBody("x", fresh, DirectEdit{}); err != nil || same != fresh {
		t.Errorf("a no-op edit rewrote the file (%v):\n%s\n---\n%s", err, fresh, same)
	}
}

// Changing the preset re-maps the parameters; fine-tuning the same request
// sends wins; what no preset defines stays.
func TestChangingThePresetRemapsThePckParameters(t *testing.T) {
	spec := pckIranSpec()
	spec.PckConns, spec.PckKCPMode, spec.PckKCPSndWnd, spec.PckKCPRcvWnd = 3, "normal", 512, 512
	spec.PckKCPBlock, spec.PckKCPMTU = "salsa20", 1300
	spec.SockBuf = 4 << 20
	original := spec.Render()
	effective := func(body string) paqet.Params {
		t.Helper()
		l := decode(t, body).L3
		engine, err := l3.ConfigFromTable(l)
		if err != nil {
			t.Fatal(err)
		}
		pc, err := l3.PckEngineConfig(engine)
		if err != nil {
			t.Fatal(err)
		}
		return pc.Params
	}
	if p := effective(original); p.Conns != 3 || p.Mode != "normal" || p.SndWnd != 512 {
		t.Fatalf("the fine-tuning does not apply before the change: %+v", p)
	}

	agg := PresetAggressive
	body, err := editedDirectBody("x", original, DirectEdit{Preset: &agg})
	if err != nil {
		t.Fatal(err)
	}
	p := effective(body)
	want, _ := paqet.PresetParams(paqet.PresetAggressive)
	if p.Conns != want.Conns || p.Mode != want.Mode || p.SndWnd != want.SndWnd || p.RcvWnd != want.RcvWnd || p.PcapBuf != want.PcapBuf {
		t.Errorf("the tunnel did not move to Aggressive's parameters: %+v, want %+v", p, want)
	}
	if p.Block != "salsa20" || p.MTU != 1300 {
		t.Errorf("a preset change reset the cipher or the packet size, which no preset defines: %+v", p)
	}

	// A value sent with the preset still wins.
	two := 2
	body, err = editedDirectBody("x", original, DirectEdit{Preset: &agg, PckConns: &two})
	if err != nil {
		t.Fatal(err)
	}
	if p := effective(body); p.Conns != 2 || p.Mode != want.Mode {
		t.Errorf("an explicit value sent with the preset was not kept: %+v", p)
	}

	// A preset sent unchanged resets nothing.
	turbo := PresetTurbo
	if body, err = editedDirectBody("x", original, DirectEdit{Preset: &turbo}); err != nil || effective(body).Conns != 3 {
		t.Errorf("the same preset reset the fine-tuning (%v)", err)
	}
	// An unknown one is refused by name.
	bad := "warp"
	if _, err := editedDirectBody("x", original, DirectEdit{Preset: &bad}); err == nil || !strings.Contains(err.Error(), "balance, turbo, aggressive") {
		t.Errorf("an unknown preset = %v", err)
	}
}

// The panel edit refuses what a TUN-less tunnel cannot have, in words, and
// refuses fine-tuning on a carrier that has none.
func TestThePanelEditRefusesTunSettingsOnPck(t *testing.T) {
	original := pckIranSpec().Render()
	mtu, on := 1300, true
	for name, e := range map[string]DirectEdit{
		"mtu":          {MTU: &mtu},
		"auto mtu":     {AutoMTU: &on},
		"fec":          {FEC: &on},
		"a bad cipher": {PckKCPBlock: strPtr("rot13")},
	} {
		if _, err := editedDirectBody("x", original, e); err == nil {
			t.Errorf("%s was accepted on a PCK tunnel", name)
		}
	}
	xdi := l3Spec{Name: "t", Side: sideIran, Carrier: "xdi", Encap: "gre", Addr: "1.2.3.4:9000", Token: "t",
		Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400, Ports: []string{"443"}}.Render()
	conns := 2
	if _, err := editedDirectBody("t", xdi, DirectEdit{PckConns: &conns}); err == nil ||
		!strings.Contains(err.Error(), "no pck_* settings") {
		t.Errorf("pck_* on an xDi tunnel = %v", err)
	}
}

func strPtr(s string) *string { return &s }

// PCK is not edited into the TUN carriers, nor they into it: the reason is the
// addresses and the TUN, which the edit cannot make the other server agree on.
func TestACarrierChangeAcrossPckAndTheTunCarriersIsRefusedWithAReason(t *testing.T) {
	pckL := decode(t, pckIranSpec().Render()).L3
	xdiL := decode(t, l3Spec{Name: "t", Side: sideIran, Carrier: "xdi", Encap: "gre", Addr: "1.2.3.4:9000", Token: "t",
		Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400, Ports: []string{"443"}}.Render()).L3
	for _, to := range []string{"xdi", "sni", "spoof"} {
		to := to
		_, err := applyDirectEdit(pckL, DirectEdit{Carrier: &to})
		if err == nil || !strings.Contains(err.Error(), "TUN") || !strings.Contains(err.Error(), "delete this one") {
			t.Errorf("pck → %s = %v, want a refusal that names the TUN and says to add a new tunnel", to, err)
		}
	}
	pck := "pck"
	if _, err := applyDirectEdit(xdiL, DirectEdit{Carrier: &pck}); err == nil || !strings.Contains(err.Error(), "routed traffic") {
		t.Errorf("xdi → pck = %v, want the loss of routed traffic named", err)
	}
	// The TUN carriers still move among themselves, and pck stays on pck.
	sni := "sni"
	if l, err := applyDirectEdit(xdiL, DirectEdit{Carrier: &sni}); err != nil || l.Carrier != "sni" {
		t.Errorf("xdi → sni = %v", err)
	}
	if _, err := applyDirectEdit(pckL, DirectEdit{Carrier: &pck}); err != nil {
		t.Errorf("pck → pck = %v", err)
	}
	// The menu offers a PCK tunnel PCK alone.
	set := directSettingsFrom("t", pckL)
	if len(set.Carriers) != 1 || set.Carriers[0]["value"] != "pck" {
		t.Errorf("a PCK tunnel's carrier menu offers %v", set.Carriers)
	}
	if !set.Pck || set.PckEffective == "" || !strings.Contains(set.PckEffective, "sessions") {
		t.Errorf("the panel's settings do not describe the engine: %+v", set)
	}
}

// PckNotes reach the panel for a migrated tunnel.
func TestAMigratedTunnelsNotesReachThePanel(t *testing.T) {
	l := decode(t, `[l3]
mode = "dial"
addr = "203.0.113.9:9000"
token = "a-long-token-for-pck"
carrier = "pck"
iface = "bp1"
local_ip = "10.10.1.1/30"
peer_ip = "10.10.1.2"
fec_data = 10
fec_parity = 3
`).L3
	set := directSettingsFrom("old", l)
	if len(set.PckNotes) == 0 || !strings.Contains(strings.Join(set.PckNotes, " "), "fec") {
		t.Errorf("a migrated tunnel's notes: %v", set.PckNotes)
	}
}

// A setup link carries what the kharej needs and produces a valid kharej file.
func TestAPckSetupLinkBuildsTheKharejEnd(t *testing.T) {
	spec := pckIranSpec()
	spec.PckConns, spec.PckKCPMode, spec.PckKCPBlock, spec.PckKCPMTU = 3, "fast3", "aes-128-gcm", 1300
	spec.Preset = PresetAggressive
	raw := pendingShareLink(spec, linkExtras{})
	if !strings.HasPrefix(raw, shareScheme) {
		t.Fatalf("no link: %q", raw)
	}
	link, err := DecodeShareLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	if link.Tr != "pck" || link.Tok != spec.Token || link.Port != "9000" || link.Preset != "aggressive" ||
		link.PckConns != 3 || link.PckKCPBlock != "aes-128-gcm" {
		t.Fatalf("the link lacks what the kharej needs: %+v", link)
	}
	if link.LocalIP != "" || link.PeerIP != "" || link.Encap != "" || link.MTU != 0 || link.FECData != 0 {
		t.Errorf("a PCK link carries TUN settings: %+v", link)
	}

	form := MirrorForPeer(link)
	if form.Side != "kharej" || form.Token != spec.Token || form.TunnelPort != "9000" || form.Carrier != "pck" {
		t.Fatalf("the mirrored form: %+v", form)
	}
	for _, p := range []string{"token", "tunnelPort", "carrier", "pckKcpBlock", "pckKcpMtu"} {
		found := false
		for _, q := range form.Paired {
			found = found || q == p
		}
		if !found {
			t.Errorf("%q is not marked as paired: %v", p, form.Paired)
		}
	}
	kharej, err := form.ToNewDirectTunnel().spec()
	if err != nil {
		t.Fatal(err)
	}
	body := kharej.Render()
	cfg := decode(t, body).L3
	if cfg.Mode != "listen" || cfg.Addr != "0.0.0.0:9000" || cfg.Token != spec.Token || cfg.Carrier != "pck" ||
		cfg.Preset != "aggressive" || cfg.PckConns != 3 || cfg.PckKCPMode != "fast3" || cfg.PckKCPMTU != 1300 ||
		cfg.PckKCPBlock != "aes-128-gcm" || len(cfg.Ports) != 0 {
		t.Fatalf("the kharej file is not the mirror of the Iran one:\n%s", body)
	}
	engine, err := l3.ConfigFromTable(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Validate(); err != nil {
		t.Errorf("the engine refuses the kharej file the link built: %v", err)
	}
	// Both ends derive the same parameters from it.
	iran, _ := l3.ConfigFromTable(decode(t, spec.Render()).L3)
	pcI, _ := l3.PckEngineConfig(iran)
	pcK, _ := l3.PckEngineConfig(engine)
	if pcI.Params != pcK.Params || pcI.Token != pcK.Token || pcK.Server != true || pcI.Server != false {
		t.Errorf("the two ends run different engines:\n%+v\n%+v", pcI, pcK)
	}

	// The same link survives the compact format.
	v2, err := encodeShareLinkV2(link)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeShareLink(v2)
	if err != nil || back.PckKCPBlock != "aes-128-gcm" || back.PckConns != 3 || back.PckKCPMode != "fast3" {
		t.Errorf("format 2 lost the pck settings: %+v (%v)", back, err)
	}
}

// An Iran server that has not been updated still writes the TUN's settings into
// its link; the kharej builds a PCK file from it all the same.
func TestALinkFromAnOldPckIranServerStillBuildsAKharej(t *testing.T) {
	off := false
	sent := ShareLink{
		Kind: "direct", From: "iran", Name: "old", Tok: strings.Repeat("k", 64), Tr: "pck", Encap: "gre", Port: "5376",
		LocalIP: "10.10.2.1/30", PeerIP: "10.10.2.2", FECData: 10, FECParity: 3, MTU: 1380, GREKey: 7, L3MSS: 1300,
		AutoMTU: &off, Preset: "turbo", Ports: "443", AcceptUDP: true,
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
	if form.LocalIP != "" || form.MTU != 0 || form.FEC || form.GREKey != 0 {
		t.Errorf("the TUN's settings were carried into a PCK form: %+v", form)
	}
	spec, err := form.ToNewDirectTunnel().spec()
	if err != nil {
		t.Fatal(err)
	}
	if body := spec.Render(); strings.Contains(body, "local_ip") || strings.Contains(body, "fec_data") {
		t.Errorf("a PCK file from an old link carries TUN keys:\n%s", body)
	}
}

// A PCK tunnel claims no interface and no subnet, so a TUN tunnel set up after
// it does not skip a name or a block for it; and it is never shared into.
func TestAPckTunnelClaimsNoInterfaceOrSubnet(t *testing.T) {
	existing := []config.L3Config{{Carrier: "pck", Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", Mode: "dial"}}
	if got := freeL3IfaceIn(existing); got != "bp0" {
		t.Errorf("a PCK tunnel claimed %s", got)
	}
	if local, _ := freeL3SubnetIn(existing, sideIran); local != "10.10.0.1/30" {
		t.Errorf("a PCK tunnel claimed a block: %s", local)
	}
}

// The CLI's Edit for a PCK tunnel shows no interface and no MTU, and offers the
// engine's settings in their place.
func TestTheCLIEditOfAPckTunnelHasNoTunRows(t *testing.T) {
	l := decode(t, pckIranSpec().Render()).L3
	restore := tui.SetInput(strings.NewReader("0\n"))
	out := capture(t, func() { editL3Ports(Tunnel{Name: "p"}, config.Config{L3: l}) })
	restore()
	for _, want := range []string{"Direct (PCK)", "KCP over crafted TCP", "2 sessions, fast2", "Forwarded Ports", "Preset", "Fine-Tune", "Show Token"} {
		if !strings.Contains(out, want) {
			t.Errorf("the edit screen lacks %q:\n%s", want, out)
		}
	}
	for _, never := range []string{"Interface", "MTU", "MSS", "GRE", "Carrier  "} {
		if strings.Contains(out, never) {
			t.Errorf("the PCK edit screen shows %q:\n%s", never, out)
		}
	}
}

// pck_remote_targets is the kharej's own decision; an edit that re-renders its
// file must not take it away.
func TestAnEditKeepsTheKharejsPckRemoteTargets(t *testing.T) {
	body := `[l3]
mode = "listen"
addr = "0.0.0.0:9000"
token = "a-long-token-for-pck"
carrier = "pck"
preset = "turbo"
pck_remote_targets = true
`
	l := decode(t, body).L3
	out := directSpecFrom("k", l).Render()
	back := decode(t, out).L3
	if !back.PckRemoteTargets {
		t.Fatalf("the re-rendered kharej file lost pck_remote_targets:\n%s", out)
	}
	engine, err := l3.ConfigFromTable(back)
	if err != nil {
		t.Fatal(err)
	}
	pc, _ := l3.PckEngineConfig(engine)
	if !pc.RemoteTargets {
		t.Fatal("the engine was not told")
	}
	// Absent, it is not written: the default is the safe one.
	l.PckRemoteTargets = false
	off := directSpecFrom("k", l).Render()
	if regexp.MustCompile(`(?m)^pck_remote_targets\s*=`).MatchString(off) || decode(t, off).L3.PckRemoteTargets {
		t.Fatalf("pck_remote_targets written while off:\n%s", off)
	}
}
