package manage

import "testing"

// A card shows two facts, so the API carries two fields.
//
// It used to carry one, and the panel printed it raw: "l3/pck". That is the
// name this code uses internally, and it leaked onto the screen because there
// was a single field where there were always two facts — which way the tunnel
// was built, and what carries it.
func TestATunnelDescribesItselfInTwoParts(t *testing.T) {
	for _, c := range []struct {
		transport string
		direction string
		carrier   string
	}{
		// The direct kinds carry their carrier behind a prefix.
		{"l3/pck", "direct", "pck"},
		{"l3/udp", "direct", "udp"},
		{"l3/spoof", "direct", "spoof"},
		{"l3/xdi", "direct", "xdi"},
		{"direct/tcp", "direct", "tcp"},
		{"direct/wss", "direct", "wss"},

		// A reverse transport has no prefix and must come through untouched.
		{"tcp", "reverse", "tcp"},
		{"tcpmux", "reverse", "tcpmux"},
		{"kcp", "reverse", "kcp"},
		{"wssmux", "reverse", "wssmux"},
		{"spoof", "reverse", "spoof"},
		{"pck", "reverse", "pck"},
	} {
		tun := Tunnel{Transport: c.transport}
		if got := TunnelDirection(tun); got != c.direction {
			t.Errorf("%s: direction = %q, want %q", c.transport, got, c.direction)
		}
		if got := TunnelCarrier(tun); got != c.carrier {
			t.Errorf("%s: carrier = %q, want %q", c.transport, got, c.carrier)
		}
	}
}

// The internal prefix must never reach a card again.
func TestTheCardNeverShowsAnInternalName(t *testing.T) {
	for _, transport := range []string{"l3/pck", "direct/tcp", "l3/spoof"} {
		if got := TunnelCarrier(Tunnel{Transport: transport}); got == transport {
			t.Errorf("%s reached the card unchanged", transport)
		}
	}
}
