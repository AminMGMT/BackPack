package manage

import (
	"fmt"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
)

// What a config written here must survive.
//
// These are all one failure in two shapes: the file is written by rendering a
// spec, so anything the spec cannot represent is not written — and on an edit,
// which re-renders, anything not written is deleted. Both halves are silent,
// and both produce a tunnel that is not the one the operator had.

// A token is copied between two machines by hand, so it can contain anything a
// person can type. A backslash is an escape character in a TOML basic string
// exactly as it is in a Go one, and writing one out raw either breaks the file
// or changes the token — and a changed token is answered with silence, so it
// presents as a blocked port rather than as the typo it is.
func TestTokensSurviveBeingWritten(t *testing.T) {
	for _, token := range []string{
		`plain-token-1234`,
		`with\backslash`,
		`with"quote`,
		`both\and"together`,
		`trailing\`,
	} {
		body := l3Spec{
			Side: sideKharej, Carrier: "xdi", Addr: "0.0.0.0:9000", Token: token,
			Iface: "bp0", LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1400,
		}.Render()

		var back config.Config
		if _, err := toml.Decode(body, &back); err != nil {
			t.Errorf("token %q produced a config that does not parse: %v", token, err)
			continue
		}
		if back.L3.Token != token {
			t.Errorf("token %q was written and read back as %q", token, back.L3.Token)
		}
	}
}

// Changing the MTU must not revert the carrier.
//
// The editor re-renders the whole file from a spec, so every key the spec does
// not hold is dropped by an edit that had nothing to do with it. A spoof
// carrier is a dozen keys tuned against one particular route; losing them to an
// MTU change is silent, and the tunnel that comes back up is not the one that
// went down.
func TestEditingALayer3TunnelKeepsItsCarrier(t *testing.T) {
	original := `
[l3]
mode                = "dial"
addr                = "1.2.3.4:9000"
token               = "tok"
carrier             = "spoof"
encap               = "gre"
gre_key             = 7
local_ip            = "10.10.0.1/30"
peer_ip             = "10.10.0.2"
mtu                 = 1400
sockbuf             = 8388608
spoof_profile       = "tcp"
spoof_src_ip        = "9.9.9.9"
spoof_peer_ip       = "5.6.7.8"
spoof_padding       = true
spoof_padding_max   = 128
spoof_shuffle_port  = true
spoof_xdp_interface = "eth0"
pck_interface       = "eth0"
pck_flags           = ["PA", "A"]
`
	var before config.Config
	if _, err := toml.Decode(original, &before); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The one edit the menu offers on both sides.
	changed := before.L3
	changed.MTU = 1300

	var after config.Config
	if _, err := toml.Decode(renderL3For(changed), &after); err != nil {
		t.Fatalf("the edit produced a config that does not parse: %v", err)
	}

	if after.L3.MTU != 1300 {
		t.Errorf("the edit did not take: mtu = %d", after.L3.MTU)
	}
	changed.MTU = after.L3.MTU // the one field that is meant to differ
	if diff := compareL3(changed, after.L3); diff != "" {
		t.Errorf("editing the MTU changed something else: %s", diff)
	}
}

// renderL3For is what saveL3 writes, reduced to the rendering so a test can
// call it without a tunnel on disk. It used to be a hand-written copy of the
// mapping, which could stay right while the real one drifted; it is the real
// one now.
func renderL3For(l config.L3Config) string { return directSpecFrom("", l).Render() }

// compareL3 names the first field that differs, so a failure says which key was
// lost rather than printing two structs.
func compareL3(want, have config.L3Config) string {
	for _, c := range []struct {
		key        string
		want, have any
	}{
		{"carrier", want.Carrier, have.Carrier},
		{"encap", want.Encap, have.Encap},
		{"gre_key", want.GREKey, have.GREKey},
		{"local_ip", want.LocalIP, have.LocalIP},
		{"peer_ip", want.PeerIP, have.PeerIP},
		{"sockbuf", want.SockBuf, have.SockBuf},
		{"spoof_profile", want.SpoofProfile, have.SpoofProfile},
		{"spoof_src_ip", want.SpoofSrcIP, have.SpoofSrcIP},
		{"spoof_peer_ip", want.SpoofPeerIP, have.SpoofPeerIP},
		{"spoof_padding", want.SpoofPadding, have.SpoofPadding},
		{"spoof_padding_max", want.SpoofPaddingMax, have.SpoofPaddingMax},
		{"spoof_shuffle_port", want.SpoofShufflePort, have.SpoofShufflePort},
		{"spoof_xdp_interface", want.SpoofXDPInterface, have.SpoofXDPInterface},
		{"pck_interface", want.PckInterface, have.PckInterface},
	} {
		if c.want != c.have {
			return fmt.Sprintf("%s: was %v, now %v", c.key, c.want, c.have)
		}
	}
	if strings.Join(want.PckFlags, ",") != strings.Join(have.PckFlags, ",") {
		return fmt.Sprintf("pck_flags: was %v, now %v", want.PckFlags, have.PckFlags)
	}
	return ""
}
