package manage

import (
	"encoding/json"
	"testing"
)

func postedTune(t *testing.T, js string) *FineTune {
	t.Helper()
	var f FineTune
	if err := json.Unmarshal([]byte(js), &f); err != nil {
		t.Fatal(err)
	}
	return &f
}

// The Edit form posts every Fine Tune field, filled with the tunnel's current
// values. Changing the preset on that form must give the new preset's numbers
// and name — it used to put the old preset's numbers back over it and clear
// the name, so a tunnel moved to Turbo stayed on Balance's settings.
func TestAPresetChangedOnTheEditFormTakes(t *testing.T) {
	var s TunnelSpec
	s.Transport = "kcp"
	ApplyPreset(&s, PresetBalance)
	shown := tuneOf(s)
	form, _ := json.Marshal(shown) // everything the form displays, posted back

	posted := postedTune(t, string(form)).changedFrom(shown)
	ApplyPreset(&s, PresetTurbo)
	if len(posted.sent) > 0 {
		posted.apply(&s)
	}

	var want TunnelSpec
	want.Transport = "kcp"
	ApplyPreset(&want, PresetTurbo)
	if s.Preset != PresetTurbo {
		t.Fatalf("preset is %q after choosing Turbo", s.Preset)
	}
	if s.ConnectionPool != want.ConnectionPool || s.KCPInterval != want.KCPInterval ||
		s.KeepAlive != want.KeepAlive || s.Heartbeat != want.Heartbeat {
		t.Fatalf("Balance's numbers came back over Turbo: %+v", tuneOf(s))
	}
}

// A number changed on the same form still wins over the preset, and makes the
// tunnel custom — the one case where the preset's name should go.
func TestAKnobChangedOnTheEditFormWinsAndMakesItCustom(t *testing.T) {
	var s TunnelSpec
	s.Transport = "kcp"
	ApplyPreset(&s, PresetTurbo)
	shown := tuneOf(s)
	edited := shown
	edited.ConnectionPool = shown.ConnectionPool + 5
	form, _ := json.Marshal(edited)
	posted := postedTune(t, string(form)).changedFrom(shown)
	posted.apply(&s)
	if s.ConnectionPool != shown.ConnectionPool+5 || s.Preset != "" {
		t.Fatalf("pool %d preset %q", s.ConnectionPool, s.Preset)
	}
}

// Saving the form with only the UDP switch changed keeps the preset.
func TestAnUnrelatedSwitchKeepsThePreset(t *testing.T) {
	var s TunnelSpec
	s.Transport = "tcpmux"
	ApplyPreset(&s, PresetTurbo)
	shown := tuneOf(s)
	edited := shown
	edited.AcceptUDP = !shown.AcceptUDP
	form, _ := json.Marshal(edited)
	posted := postedTune(t, string(form)).changedFrom(shown)
	posted.apply(&s)
	if s.Preset != PresetTurbo || s.AcceptUDP == shown.AcceptUDP {
		t.Fatalf("preset %q acceptUDP %v", s.Preset, s.AcceptUDP)
	}
}

// The Add form posts only the boxes that were filled. One switch touched must
// not read every other knob as zero — heartbeat off, Nagle on, FEC off — nor
// clear the preset.
func TestAPartlyFilledAddFormLeavesThePresetAlone(t *testing.T) {
	var s TunnelSpec
	s.Transport = "kcp"
	ApplyPreset(&s, PresetTurbo)
	before := s
	postedTune(t, `{"acceptUDP": true}`).apply(&s)
	if s.Heartbeat != before.Heartbeat || s.Nodelay != before.Nodelay ||
		s.KCPDataShards != before.KCPDataShards || s.Preset != PresetTurbo {
		t.Fatalf("a missing key was read as zero: heartbeat %d nodelay %v shards %d preset %q",
			s.Heartbeat, s.Nodelay, s.KCPDataShards, s.Preset)
	}
	if !s.AcceptUDP {
		t.Fatal("the one switch that was sent was not applied")
	}
}
