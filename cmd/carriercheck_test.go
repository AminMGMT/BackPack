package cmd

import (
	"testing"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tunnel/l3"
)

// A carrier's startup checks have to run for the configurations that actually
// use it.
//
// checkPck and checkXdi were written when pck and xdi were reverse transports,
// and they gate on [server]/[client] naming one. Both are direct-tunnel
// carriers now — which is what the setup wizard writes — and a `[l3]` config
// with carrier = "pck" went past every one of them: no Linux check, no root
// check, no validation of pck_flags, pck_gateway_mac or pck_interface, and not
// a word about iptables.
//
// What that cost is the difference between "run as root, or grant CAP_NET_RAW
// with this command" at load and a socket error from inside the carrier once
// the tunnel is already supposed to be up.
func TestTheCarrierChecksSeeADirectTunnel(t *testing.T) {
	for _, tc := range []struct {
		carrier  string
		wantsPck bool
		wantsXdi bool
	}{
		{l3.CarrierPck, true, false},
		// sni is pck with a ClientHello in front of it: the same packet socket
		// and the same pck_* keys.
		{l3.CarrierSNI, true, false},
		{l3.CarrierXdi, false, true},
		{l3.CarrierUDP, false, false},
		{l3.CarrierQuic, false, false},
	} {
		t.Run(tc.carrier, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.L3.Mode = "listen"
			cfg.L3.Carrier = tc.carrier

			if got := l3Carrier(cfg) == l3.CarrierPck || l3Carrier(cfg) == l3.CarrierSNI; got != tc.wantsPck {
				t.Errorf("carrier %q: the pck checks %s run", tc.carrier,
					map[bool]string{true: "do not", false: "do"}[tc.wantsPck])
			}
			if got := l3Carrier(cfg) == l3.CarrierXdi; got != tc.wantsXdi {
				t.Errorf("carrier %q: the xdi checks %s run", tc.carrier,
					map[bool]string{true: "do not", false: "do"}[tc.wantsXdi])
			}
		})
	}
}

// The carrier is read the same way wherever it is asked about, including the
// spellings a hand-edited config arrives in.
func TestTheCarrierIsReadTheSameWayEverywhere(t *testing.T) {
	for _, written := range []string{"pck", "PCK", " Pck ", "\tpck\n"} {
		cfg := &config.Config{}
		cfg.L3.Mode = "dial"
		cfg.L3.Carrier = written
		if got := l3Carrier(cfg); got != l3.CarrierPck {
			t.Errorf("carrier written as %q reads as %q", written, got)
		}
	}
}

// A configuration that is not a direct tunnel has no carrier, however its l3
// table happens to be filled in.
func TestAReverseTunnelHasNoCarrier(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.BindAddr = "0.0.0.0:443"
	cfg.Server.Transport = config.TCP
	// Present but not enabled: Mode is what turns the layer-3 engine on, and a
	// carrier left in the file from an earlier shape of it must not make these
	// checks fire on a tunnel that never reaches that engine.
	cfg.L3.Carrier = l3.CarrierPck
	if got := l3Carrier(cfg); got != "" {
		t.Errorf("a reverse tunnel reported carrier %q", got)
	}
}
