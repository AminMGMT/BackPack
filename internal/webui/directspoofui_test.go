package webui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/manage"
)

// The direct form is the panel's half of the forged-source carrier. Until this,
// it could create a spoof tunnel and set exactly one thing about it — the peer
// address — while the CLI asked six questions. A form that offers a carrier and
// then cannot configure it is worse than one that does not offer it: the tunnel
// gets built on defaults nobody chose.

// Every field the form collects must be one the server takes. A form posting a
// key the handler refuses fails the whole create with a decoding error, which
// reads to the operator as "the panel is broken".
func TestDirectFormFieldsReachTheServer(t *testing.T) {
	body := string(dashboardHTML)

	// The keys the submit builds, read out of the source so the test breaks when
	// the form changes rather than when somebody remembers to update it.
	form := between(body, "async function submitDirect(){", "const btn=$('addsave')")
	if form == "" {
		t.Fatal("the direct submit could not be found")
	}
	for _, key := range []string{"paths:", "fec:", "stealth:", "spoof="} {
		k := strings.TrimSuffix(key, "=")
		if !strings.Contains(form, k) {
			t.Errorf("the form never sends %q", k)
		}
	}

	// And they decode into the type the handler unmarshals into.
	payload := map[string]any{
		"side": "iran", "carrier": "spoof", "name": "t", "token": "x",
		"peerAddr": "1.2.3.4", "tunnelPort": "9000", "ports": "443",
		"acceptUdp": false, "preset": "turbo", "spoofPeerIp": "1.2.3.4",
		"paths": 4, "fec": true, "stealth": true,
		"spoof": map[string]any{"profile": "udp", "srcIPs": "203.0.113.10", "peerIP": "1.2.3.4"},
	}
	raw, _ := json.Marshal(payload)
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manage.NewDirectTunnel{}); err != nil {
		t.Errorf("the form posts a field the server would refuse: %v", err)
	}
}

// The carrier's own settings are answered on BOTH ends — they are paired, and a
// pair set on one machine only is a tunnel that comes up and carries nothing.
// The peer address is the one exception: only the listening side needs it.
func TestTheCarrierSettingsAreOfferedOnBothSides(t *testing.T) {
	body := string(dashboardHTML)

	toggle := between(body, "function onDirectCarrier(){", "\n}")
	if toggle == "" {
		t.Fatal("onDirectCarrier could not be found")
	}
	// d-spoof is the kharej-only peer address; d-spoofboth is everything both
	// ends answer. If the paired settings were gated on the side, one end would
	// silently keep the defaults.
	if !strings.Contains(toggle, "d-spoofboth") {
		t.Error("the paired carrier settings are not shown; only the peer address is")
	}
	if !strings.Contains(toggle, "onKharej") {
		t.Error("the peer address is no longer restricted to the side that needs it")
	}
	if !strings.Contains(toggle, "d-udponly") {
		t.Error("spreading over sockets is not restricted to the udp carrier")
	}
}

// The profile menu is filled from the server's list, so the panel and the CLI
// cannot come to offer different profiles.
func TestTheProfileMenuComesFromTheServer(t *testing.T) {
	body := string(dashboardHTML)
	if !strings.Contains(body, "fillSel($('df-spoofprofile'),o.spoofProfiles") {
		t.Error("the profile menu is not filled from the server's list")
	}
	if len(manage.SpoofProfiles()) == 0 {
		t.Error("the server offers no profiles for it to be filled from")
	}
}

// The edit screen is the other half: a tunnel built with a forged source that
// cannot then be changed sends the operator back to the CLI, which is the exact
// gap the panel exists to close. These pin that the settings are shown, filled,
// and sent — and sent only when they moved, because an unchanged field counted
// as a change restarts the tunnel for nothing.
func TestDirectEditOffersTheCarrierSettings(t *testing.T) {
	body := string(dashboardHTML)

	for _, id := range []string{`id="edf-spoofprofile"`, `id="edf-spoofsrc"`,
		`id="edf-stealth"`, `id="edf-paths"`, `id="edf-fec"`} {
		if !strings.Contains(body, id) {
			t.Errorf("the edit form has no %s", id)
		}
	}

	open := between(body, "async function openDirectEdit(d){", "\n}")
	if open == "" {
		t.Fatal("openDirectEdit could not be found")
	}
	// Shown only for the carrier that has them, and filled from what the tunnel
	// actually runs rather than left on the form's defaults.
	for _, want := range []string{"edf-spoof", "edf-udponly", "d.spoof", "d.stealth", "d.paths", "d.fec"} {
		if !strings.Contains(open, want) {
			t.Errorf("openDirectEdit does not use %q", want)
		}
	}
	if !strings.Contains(open, "o.spoofProfiles") {
		t.Error("the edit form's profile menu is not filled from the server's list")
	}
}

// What the edit sends must decode into DirectEdit, whose pointers are what let
// an absent key and a deliberate zero be told apart.
func TestDirectEditFieldsReachTheServer(t *testing.T) {
	payload := map[string]any{
		"ports": "443", "acceptUdp": true, "preset": "turbo",
		"mtu": 1400, "autoMtu": false, "maxConnections": 0, "bandwidthMbps": 0,
		"paths": 4, "fec": true, "stealth": true,
		"spoof": map[string]any{"profile": "icmp", "srcIPs": "203.0.113.10"},
	}
	raw, _ := json.Marshal(payload)
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manage.DirectEdit{}); err != nil {
		t.Errorf("the edit posts a field the server would refuse: %v", err)
	}
}

// A dialog opened and saved with nothing touched must send nothing, or every
// visit restarts the tunnel.
func TestDirectEditSendsOnlyWhatMoved(t *testing.T) {
	body := string(dashboardHTML)
	submit := between(body, "async function submitDirectEdit(){", "\n}")
	if submit == "" {
		t.Fatal("submitDirectEdit could not be found")
	}
	// Each of the new settings is compared against what the tunnel already has.
	for _, guard := range []string{"!==!!d.stealth", "!==(d.paths||1)", "!==!!d.fec"} {
		if !strings.Contains(submit, guard) {
			t.Errorf("a setting is sent without checking it changed: missing %q", guard)
		}
	}
	if !strings.Contains(submit, "Nothing has been changed yet.") {
		t.Error("an untouched save is not refused, so it would restart the tunnel for nothing")
	}
}
