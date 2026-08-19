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
		body := directSpec{
			Side: sideKharej, Transport: "tcp", Addr: "0.0.0.0:8443", Token: token,
		}.render()

		var back config.Config
		if _, err := toml.Decode(body, &back); err != nil {
			t.Errorf("token %q produced a config that does not parse: %v", token, err)
			continue
		}
		if back.Direct.Token != token {
			t.Errorf("token %q was written and read back as %q", token, back.Direct.Token)
		}
	}

	// And the same for the layer-3 renderer, which shares the quoting but not
	// the rest of the file.
	body := l3Spec{
		Side: sideIran, Carrier: "udp", Encap: "ipip", Addr: "1.2.3.4:9000",
		Token: `l3\token`, Iface: "bp0", LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
	}.render()
	var back config.Config
	if _, err := toml.Decode(body, &back); err != nil {
		t.Fatalf("the layer-3 renderer produced a config that does not parse: %v", err)
	}
	if back.L3.Token != `l3\token` {
		t.Fatalf("layer-3 token read back as %q", back.L3.Token)
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

// The same for a direct tunnel, whose editor offers four changes and whose spec
// did not hold the certificate, the mux version or either timeout.
func TestEditingADirectTunnelKeepsWhatItWasNotAsked(t *testing.T) {
	original := `
[direct]
role              = "iran"
addr              = "1.2.3.4:8443"
token             = "tok"
transport         = "wss"
ports             = ["443"]
server_name       = "cdn.example.com"
tls_cert          = "/etc/ssl/tunnel.crt"
tls_key           = "/etc/ssl/tunnel.key"
mux_version       = 2
dial_timeout      = 20
retry_interval    = 7
keepalive_period  = 75
nodelay           = true
`
	var before config.Config
	if _, err := toml.Decode(original, &before); err != nil {
		t.Fatalf("decode: %v", err)
	}

	changed := before.Direct
	changed.Ports = []string{"443", "8080"}

	var after config.Config
	if _, err := toml.Decode(renderDirectFor(changed), &after); err != nil {
		t.Fatalf("the edit produced a config that does not parse: %v", err)
	}

	for _, c := range []struct {
		key        string
		want, have any
	}{
		{"tls_cert", before.Direct.TLSCertFile, after.Direct.TLSCertFile},
		{"tls_key", before.Direct.TLSKeyFile, after.Direct.TLSKeyFile},
		{"mux_version", before.Direct.MuxVersion, after.Direct.MuxVersion},
		{"dial_timeout", before.Direct.DialTimeout, after.Direct.DialTimeout},
		{"retry_interval", before.Direct.RetryInterval, after.Direct.RetryInterval},
		{"keepalive_period", before.Direct.Keepalive, after.Direct.Keepalive},
		{"nodelay", before.Direct.Nodelay, after.Direct.Nodelay},
		{"server_name", before.Direct.ServerName, after.Direct.ServerName},
	} {
		if c.want != c.have {
			t.Errorf("changing the ports lost %s: was %v, now %v", c.key, c.want, c.have)
		}
	}
	if len(after.Direct.Ports) != 2 {
		t.Errorf("the edit did not take: ports = %v", after.Direct.Ports)
	}
}

// Every preset must turn Nagle off.
//
// The engine calls SetNoDelay(cfg.Nodelay), so leaving the key unset does not
// leave the socket alone — it turns Nagle on, over Go's own default of off. On
// a tunnel where one mux session carries every connection, that delays every
// stream at once. The reverse presets have always set it; these did not.
func TestEveryDirectPresetDisablesNagle(t *testing.T) {
	for _, p := range directPresets {
		var spec directSpec
		p.apply(&spec)
		if !spec.Nodelay {
			t.Errorf("preset %q leaves Nagle on", p.Name)
		}

		body := directSpec{
			Side: sideIran, Transport: "tcp", Addr: "1.2.3.4:8443",
			Token: "t", Ports: []string{"443"}, Nodelay: spec.Nodelay,
			Preset: p.Name, MuxFrameSize: p.MuxFrameSize,
			MuxReceiveBuffer: p.MuxReceiveBuffer, MuxStreamBuffer: p.MuxStreamBuffer,
		}.render()
		if !strings.Contains(body, "nodelay") {
			t.Errorf("preset %q does not write nodelay into the file", p.Name)
		}
	}
}

// renderL3For and renderDirectFor are what saveL3 and saveDirect do, reduced to
// the rendering so a test can call it without a tunnel on disk. They must stay
// in step with those two; the tests above are what notices if they do not.
func renderL3For(l config.L3Config) string {
	side := sideIran
	if strings.EqualFold(strings.TrimSpace(l.Mode), "listen") {
		side = sideKharej
	}
	return l3Spec{
		Side: side, Carrier: orDefault(l.Carrier, "udp"),
		Encap: orDefault(l.Encap, "ipip"), GREKey: l.GREKey,
		Addr: l.Addr, Token: l.Token, Iface: orDefault(l.Iface, "bp0"),
		LocalIP: l.LocalIP, PeerIP: l.PeerIP, MTU: l.MTU, SockBuf: l.SockBuf,
		Ports: l.Ports, AcceptUDP: l.AcceptUDP,
		MaxConnections: l.MaxConnections, BandwidthMbps: l.BandwidthMbps,
		Spoof: l.SpoofConfig, Pck: l.PckConfig,
	}.render()
}

func renderDirectFor(d config.DirectConfig) string {
	side := sideIran
	if d.ResolvedRole() == "origin" {
		side = sideKharej
	}
	return directSpec{
		Side: side, Transport: orDefault(d.Transport, "tcp"),
		Addr: d.Addr, Token: d.Token, Ports: d.Ports, AcceptUDP: d.AcceptUDP,
		MaxConnections: d.MaxConnections, BandwidthMbps: d.BandwidthMbps,
		Sessions: d.Sessions, Preset: d.Preset,
		MuxFrameSize: d.MaxFrameSize, MuxReceiveBuffer: d.MaxReceiveBuffer,
		MuxStreamBuffer: d.MaxStreamBuffer, Keepalive: d.Keepalive,
		Nodelay: d.Nodelay, ServerName: d.ServerName,
		ACMEDomain: d.ACMEDomain, ACMEEmail: d.ACMEEmail,
		TLSCertFile: d.TLSCertFile, TLSKeyFile: d.TLSKeyFile,
		MuxVersion:  d.MuxVersion,
		DialTimeout: d.DialTimeout, RetryInterval: d.RetryInterval,
	}.render()
}

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
