package tunnelspec

import (
	"strings"
	"testing"
)

// What the ports carry is written to the file, on every transport that
// forwards ports over a stream — the plain engine's mux and websocket ones
// included — and the udp transport offers UDP alone.
func TestThePortProtocolIsRenderedAndNormalised(t *testing.T) {
	for _, tr := range []string{"tcp", "tcpmux", "ws", "wss", "wsmux", "wssmux", "kcp", "quic"} {
		s := Spec{Role: "server", Transport: tr, Ports: []string{"443"}}
		if got := s.PortProtocols(); len(got) != 3 {
			t.Fatalf("%s offers %v, want tcp, udp and both", tr, got)
		}
		for _, proto := range []string{"tcp", "udp", "both"} {
			s.SetPortProtocol(proto)
			if got := s.PortProtocol(); got != proto {
				t.Fatalf("%s: set %q, read %q", tr, proto, got)
			}
			if out := s.Render(); !strings.Contains(out, `port_protocol = "`+proto+`"`) {
				t.Fatalf("%s: %q not written:\n%s", tr, proto, out)
			}
		}
	}

	s := Spec{Role: "server", Transport: "udp", Ports: []string{"443"}}
	if got := s.PortProtocols(); len(got) != 1 || got[0] != "udp" {
		t.Fatalf("udp offers %v, want [udp]", got)
	}
}
