package manage

import (
	"strings"
	"testing"

	"github.com/backpack/backpack/config"
)

// The panel's half of the forged-source carrier. The CLI has a screen for it;
// these pin that the panel can reach the same settings, and that it refuses the
// two things that would produce a tunnel nobody can explain.

// A drawer sent with the form has to actually land in the tunnel that gets
// written, or the panel is a form that discards what it collected.
func TestTheDirectFormsSpoofDrawerReachesTheConfig(t *testing.T) {
	spec, err := (NewDirectTunnel{
		Side: "iran", Carrier: "spoof", Name: "iran-spoof",
		Token: "a-token-0123456789abcdefghijklmno", PeerAddr: "203.0.113.9",
		TunnelPort: "9000", Ports: "443",
		Spoof: &SpoofTune{
			Profile: "icmp", SrcIPs: "81.28.60.1, 81.28.60.2",
			PeerSrcIP: "8.8.4.4", MTU: 1400,
		},
	}).spec()
	if err != nil {
		t.Fatalf("the form was refused: %v", err)
	}
	if spec.Spoof.SpoofProfile != "icmp" {
		t.Errorf("profile = %q, want icmp", spec.Spoof.SpoofProfile)
	}
	if len(spec.Spoof.SpoofSrcPool) != 2 || spec.Spoof.SpoofSrcIP != "81.28.60.1" {
		t.Errorf("the forged sources did not land: %q %v", spec.Spoof.SpoofSrcIP, spec.Spoof.SpoofSrcPool)
	}
	if spec.Spoof.SpoofPeerSrcIP != "8.8.4.4" || spec.Spoof.SpoofMTU != 1400 {
		t.Errorf("the rest of the drawer did not land: %+v", spec.Spoof)
	}
}

// Stealth is one answer over several settings, and the panel has to mean the
// same thing by it as the wizard does.
func TestTheDirectFormsStealthAnswerTurnsTheGroupOn(t *testing.T) {
	spec, err := (NewDirectTunnel{
		Side: "iran", Carrier: "spoof", Name: "iran-spoof",
		Token: "a-token-0123456789abcdefghijklmno", PeerAddr: "203.0.113.9",
		TunnelPort: "9000", Ports: "443", Stealth: true,
	}).spec()
	if err != nil {
		t.Fatalf("the form was refused: %v", err)
	}
	if !spoofStealthOn(spec.Spoof) {
		t.Errorf("Stealth was asked for and did not arrive: %+v", spec.Spoof)
	}
}

// The listening side cannot learn where to answer, so a form that leaves it out
// is refused at the form rather than written and left to fail as a tunnel that
// comes up and sends its replies nowhere.
func TestTheKharejSideStillNeedsThePeersRealAddress(t *testing.T) {
	_, err := (NewDirectTunnel{
		Side: "kharej", Carrier: "spoof", Name: "kharej-spoof",
		Token: "a-token-0123456789abcdefghijklmno", TunnelPort: "9000", Ports: "443",
	}).spec()
	if err == nil {
		t.Fatal("a listening spoof tunnel was accepted with nowhere to send its replies")
	}
	if !strings.Contains(err.Error(), "real IP") {
		t.Errorf("the error does not say what is missing: %v", err)
	}

	// And the drawer can supply it, not only the dedicated field.
	if _, err := (NewDirectTunnel{
		Side: "kharej", Carrier: "spoof", Name: "kharej-spoof",
		Token: "a-token-0123456789abcdefghijklmno", TunnelPort: "9000", Ports: "443",
		Spoof: &SpoofTune{Profile: "udp", PeerIP: "203.0.113.9"},
	}).spec(); err != nil {
		t.Errorf("the drawer's peer address was not accepted: %v", err)
	}
}

// Editing the carrier of a tunnel that has no forged source is refused rather
// than written: spoof keys under a udp tunnel read as a tunnel doing something
// it is not.
func TestCarrierEditsAreRefusedOnACarrierWithoutOne(t *testing.T) {
	udp := config.L3Config{Mode: "dial", Carrier: "udp"}
	on := true
	if _, err := applyDirectEdit(udp, DirectEdit{Stealth: &on}); err == nil {
		t.Error("Stealth was accepted on a udp tunnel")
	}
	if _, err := applyDirectEdit(udp, DirectEdit{Spoof: &SpoofTune{Profile: "udp"}}); err == nil {
		t.Error("a carrier drawer was accepted on a udp tunnel")
	}
}

// And on a spoof tunnel the edit round-trips: what the panel writes is what it
// reads back, which is the contract the Edit screen stands on.
func TestCarrierEditsRoundTripOnASpoofTunnel(t *testing.T) {
	l := config.L3Config{Mode: "dial", Carrier: "spoof"}

	on := true
	l, err := applyDirectEdit(l, DirectEdit{
		Spoof:   &SpoofTune{Profile: "tcp", SrcIPs: "203.0.113.10"},
		Stealth: &on,
	})
	if err != nil {
		t.Fatalf("the edit was refused: %v", err)
	}
	got := directSettingsFrom("t", l)
	if got.Spoof.Profile != "tcp" || got.Spoof.SrcIPs != "203.0.113.10" {
		t.Errorf("the drawer did not read back: %+v", got.Spoof)
	}
	if !got.Stealth {
		t.Error("Stealth was turned on and read back off")
	}
	// The tcp profile is the one that carries a TLS record header, so the group
	// includes it here and the setting has to survive into the config.
	if !l.SpoofFakeTLS {
		t.Error("the tcp profile's fake TLS header was not part of the group")
	}

	off := false
	l, err = applyDirectEdit(l, DirectEdit{Stealth: &off})
	if err != nil {
		t.Fatalf("turning Stealth off was refused: %v", err)
	}
	if directSettingsFrom("t", l).Stealth {
		t.Error("Stealth was turned off and read back on")
	}
}
