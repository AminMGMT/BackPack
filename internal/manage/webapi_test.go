package manage

import (
	"strings"
	"testing"
)

// The panel's transport menu and the CLI's are one list. If they ever became
// two, a transport would exist in the terminal and not in the browser — which
// is exactly the drift this whole file exists to prevent.
func TestTransportMenuIsTheCLIMenu(t *testing.T) {
	families := TransportFamilies()
	if len(families) != len(transportGroups) {
		t.Fatalf("panel shows %d families, the CLI menu has %d", len(families), len(transportGroups))
	}
	for i, g := range transportGroups {
		f := families[i]
		if f.Label != g.label || f.Desc != g.desc {
			t.Errorf("family %d: panel says %q/%q, the menu says %q/%q", i, f.Label, f.Desc, g.label, g.desc)
		}
		var want []string
		for _, e := range g.entries {
			// An entry with no value is listed for orientation and cannot be
			// picked; a select option that does nothing is worse than no option.
			if e.value != "" {
				want = append(want, e.value)
			}
		}
		var got []string
		for _, e := range f.Entries {
			got = append(got, e.Value)
			if !validTransport(e.Value) {
				t.Errorf("the panel offers %q, which the engine does not support", e.Value)
			}
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("family %q: panel offers %v, the menu offers %v", f.Label, got, want)
		}
	}
}

// The presets shown in the panel are the presets, not a copy of them.
func TestPresetsAreTheCLIPresets(t *testing.T) {
	got := Presets()
	if len(got) != len(presetOptions) {
		t.Fatalf("panel shows %d presets, there are %d", len(got), len(presetOptions))
	}
	for i, o := range presetOptions {
		if got[i].Value != o.value || got[i].Label != o.label {
			t.Errorf("preset %d: panel says %q/%q, want %q/%q", i, got[i].Value, got[i].Label, o.value, o.label)
		}
		if !validPreset(got[i].Value) {
			t.Errorf("the panel offers preset %q, which ApplyPreset does not know", got[i].Value)
		}
	}
}

// PresetTune has to answer with the preset's own numbers, because it is what
// the Fine Tune drawer opens on — showing anything else would invite somebody
// to "keep" a value the tunnel was never going to have.
func TestPresetTuneShowsThePresetsOwnNumbers(t *testing.T) {
	var want TunnelSpec
	want.Role, want.Transport = "server", "kcp"
	ApplyPreset(&want, PresetTurbo)

	got := PresetTune(PresetTurbo, "server", "kcp")
	if got.ChannelSize != want.ChannelSize || got.KCPSndWnd != want.KCPSndWnd ||
		got.MuxCon != want.MuxCon || got.KeepAlive != want.KeepAlive {
		t.Errorf("Fine Tune would open on values the preset never chose: %+v", got)
	}
	if got.LogLevel != want.LogLevel || got.Nodelay != want.Nodelay {
		t.Errorf("log level / nodelay do not match the preset: %+v", got)
	}
}

// A form that was never opened posts zeros. Writing those through would give a
// tunnel a zero window and no keepalive — so an unanswered number keeps the
// preset's value, and only the numbers whose zero is an answer are copied.
func TestUnansweredNumbersKeepThePresetsValue(t *testing.T) {
	s := TunnelSpec{Role: "server", Transport: "kcp"}
	ApplyPreset(&s, PresetTurbo)
	before := s

	FineTune{Nodelay: true, LogLevel: "debug"}.apply(&s)

	if s.KeepAlive != before.KeepAlive || s.ChannelSize != before.ChannelSize ||
		s.MuxCon != before.MuxCon || s.KCPSndWnd != before.KCPSndWnd || s.KCPMTU != before.KCPMTU {
		t.Errorf("a blank form overwrote the preset's numbers with zeros: %+v", s)
	}
	if s.LogLevel != "debug" {
		t.Errorf("log level = %q, want the answered value", s.LogLevel)
	}
	// Heartbeat and the FEC shards are the exception: zero means "off", and the
	// CLI offers it in as many words.
	if s.Heartbeat != 0 || s.KCPDataShards != 0 || s.KCPParityShards != 0 {
		t.Errorf("a deliberate zero was ignored: heartbeat=%d shards=%d/%d",
			s.Heartbeat, s.KCPDataShards, s.KCPParityShards)
	}
	// Tuning by hand means the tunnel no longer matches any profile, so a later
	// preset change cannot silently overwrite these answers.
	if s.Preset != "" {
		t.Errorf("preset = %q after manual tuning, want it cleared", s.Preset)
	}
}

// Settings that belong to one transport must not leak onto another: zero-copy
// and accept_udp are plain-TCP only, and the engine would take them and ignore
// them, which reads as a setting that did not work.
func TestTCPOnlyKnobsStayOnTCP(t *testing.T) {
	s := TunnelSpec{Role: "server", Transport: "wssmux"}
	ApplyPreset(&s, PresetTurbo)
	FineTune{ZeroCopy: true, AcceptUDP: true}.apply(&s)
	if s.ZeroCopy || s.AcceptUDP {
		t.Errorf("a TCP-only knob was set on a %s tunnel", s.Transport)
	}

	s = TunnelSpec{Role: "server", Transport: "tcp"}
	ApplyPreset(&s, PresetTurbo)
	FineTune{ZeroCopy: true, AcceptUDP: true}.apply(&s)
	if !s.ZeroCopy || !s.AcceptUDP {
		t.Error("a TCP-only knob was refused on a TCP tunnel")
	}
}

// Every one of these is rejected before anything is written, so a bad form can
// never leave half a tunnel on disk.
func TestCreateTunnelRefusesBadFormsBeforeWriting(t *testing.T) {
	good := NewTunnel{
		Role: "server", Transport: "tcp", Name: "webapi-test-nonexistent",
		TunnelPort: "4443", Token: "abc", Ports: "443", Preset: PresetTurbo,
	}
	for _, tc := range []struct {
		why    string
		mutate func(*NewTunnel)
	}{
		{"no role", func(n *NewTunnel) { n.Role = "" }},
		{"unknown role", func(n *NewTunnel) { n.Role = "gateway" }},
		{"unknown transport", func(n *NewTunnel) { n.Transport = "carrier-pigeon" }},
		{"empty name", func(n *NewTunnel) { n.Name = "" }},
		{"name with a slash", func(n *NewTunnel) { n.Name = "../etc/passwd" }},
		{"port out of range", func(n *NewTunnel) { n.TunnelPort = "70000" }},
		{"port that is not a number", func(n *NewTunnel) { n.TunnelPort = "http" }},
		{"no token", func(n *NewTunnel) { n.Token = "  " }},
		{"no forwarded ports", func(n *NewTunnel) { n.Ports = "" }},
		{"unparseable forwarded port", func(n *NewTunnel) { n.Ports = "443=" }},
		{"client with no server address", func(n *NewTunnel) { n.Role, n.ServerAddr = "client", "" }},
	} {
		n := good
		tc.mutate(&n)
		if _, _, err := CreateTunnel(n); err == nil {
			t.Errorf("%s was accepted", tc.why)
		}
	}
}

// The suggested port has to be one somebody can actually use.
func TestSuggestedPortIsFourDigitsAndFree(t *testing.T) {
	p := SuggestPort()
	if p < 1000 || p > 9999 {
		t.Fatalf("suggested port %d is not four digits", p)
	}
}

// The suggested token is the wizard's own: 64 characters, and a different one
// every time — a token that repeated would be a shared secret that is not one.
func TestSuggestedTokenIsFreshAndFullLength(t *testing.T) {
	a, b := NewToken(), NewToken()
	if len(a) != 64 {
		t.Errorf("token is %d characters, want 64", len(a))
	}
	if a == b {
		t.Error("two suggestions produced the same token")
	}
}
