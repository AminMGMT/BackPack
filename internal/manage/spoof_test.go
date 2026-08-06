package manage

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
)

// A spoof tunnel's spec must render the spoof_* keys, and a rendered config must
// decode back into the embedded SpoofConfig — the round trip the edit flow
// depends on.

func TestSpoofServerRenderRoundTrip(t *testing.T) {
	s := TunnelSpec{
		Role:           "server",
		Name:           "iran-spoof",
		Transport:      "spoof",
		BindAddr:       "0.0.0.0:1234",
		Token:          "spoof-token-0123456789abcdefghij",
		Ports:          []string{"443"},
		SpoofProfile:   "udp",
		SpoofSrcIP:     "81.28.60.1",
		SpoofSrcPool:   []string{"81.28.60.1", "81.28.60.2"},
		SpoofPeerIP:    "38.87.117.94",
		SpoofInterface: "eth0",
	}
	out := s.Render()

	for _, want := range []string{
		`transport = "spoof"`,
		`spoof_profile = "udp"`,
		`spoof_src_ip = "81.28.60.1"`,
		`spoof_src_pool = ["81.28.60.1", "81.28.60.2"]`,
		`spoof_peer_ip = "38.87.117.94"`,
		`spoof_interface = "eth0"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q\n---\n%s", want, out)
		}
	}

	var cfg config.Config
	if _, err := toml.Decode(out, &cfg); err != nil {
		t.Fatalf("rendered spoof config does not parse: %v", err)
	}
	sc := cfg.Server.SpoofConfig
	if sc.SpoofProfile != "udp" || sc.SpoofSrcIP != "81.28.60.1" ||
		sc.SpoofPeerIP != "38.87.117.94" || sc.SpoofInterface != "eth0" ||
		len(sc.SpoofSrcPool) != 2 || sc.SpoofSrcPool[1] != "81.28.60.2" {
		t.Fatalf("spoof fields did not survive the round trip: %+v", sc)
	}
}

// The udp profile is the default, and an empty profile must render as udp so a
// hand-cleared field never leaves the carrier undefined.
func TestSpoofDefaultsToUDP(t *testing.T) {
	s := TunnelSpec{Role: "client", Name: "kharej", Transport: "spoof", RemoteAddr: "1.2.3.4:1234", Token: "t"}
	out := s.Render()
	if !strings.Contains(out, `spoof_profile = "udp"`) {
		t.Errorf("empty profile should render as udp\n---\n%s", out)
	}
}

// A non-spoof transport must never carry spoof_* keys, the same guarantee
// writeKCP gives for the kcp knobs.
func TestNonSpoofOmitsSpoofKeys(t *testing.T) {
	s := TunnelSpec{Role: "server", Name: "plain", Transport: "tcp", BindAddr: "0.0.0.0:80", Token: "t", Ports: []string{"443"}}
	if out := s.Render(); strings.Contains(out, "spoof_") {
		t.Errorf("tcp tunnel should carry no spoof keys\n---\n%s", out)
	}
}
