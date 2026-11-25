package config

import "testing"

func TestPortProtocolDecidesWhatThePortsCarry(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		proto     string
		acceptUDP *bool
		tcp, udp  bool
	}{
		{"", nil, true, false}, // a file with neither key: TCP only
		{"", &no, true, false}, // accept_udp = false
		{"", &yes, true, true}, // accept_udp = true, as files before the key
		{"tcp", &yes, true, false},
		{"udp", nil, false, true},
		{"both", &no, true, true},
	} {
		s := ServerConfig{PortProtocol: c.proto, AcceptUDP: c.acceptUDP}
		if s.ForwardsTCP() != c.tcp || s.ForwardsUDP() != c.udp {
			t.Errorf("port_protocol %q, accept_udp %v: tcp %v udp %v, want %v %v",
				c.proto, c.acceptUDP, s.ForwardsTCP(), s.ForwardsUDP(), c.tcp, c.udp)
		}
	}
	for _, v := range []string{"", "tcp", "udp", "both"} {
		if !ValidPortProtocol(v) {
			t.Errorf("%q refused", v)
		}
	}
	if ValidPortProtocol("tcp+udp") {
		t.Error("an unknown value was accepted")
	}
}
