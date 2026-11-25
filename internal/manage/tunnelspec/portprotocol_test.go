package tunnelspec

import (
	"strings"
	"testing"
)

// What the ports carry is written to the file, and a UDP-only choice never
// lands on a transport that forwards TCP only.
func TestThePortProtocolIsRenderedAndNormalised(t *testing.T) {
	s := Spec{Role: "server", Transport: "tcp", Ports: []string{"443"}}
	for _, proto := range []string{"tcp", "udp", "both"} {
		s.SetPortProtocol(proto)
		if got := s.PortProtocol(); got != proto {
			t.Fatalf("set %q, read %q", proto, got)
		}
		if out := s.Render(); !strings.Contains(out, `port_protocol = "`+proto+`"`) {
			t.Fatalf("%q not written:\n%s", proto, out)
		}
	}

	s.SetPortProtocol("udp")
	s.Transport = "tcpmux"
	if got := s.PortProtocol(); got != "tcp" {
		t.Fatalf("UDP alone on tcpmux reads %q, want tcp", got)
	}
	if got := s.PortProtocols(); len(got) != 1 || got[0] != "tcp" {
		t.Fatalf("tcpmux offers %v, want [tcp]", got)
	}
}
