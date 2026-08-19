package manage

import "testing"

// The panel must not probe a datagram carrier with a TCP connect.
//
// This is the bug that put an Iran card on "offline" while its tunnel was
// carrying traffic. The transport of a layer-3 tunnel is "l3/pck", which none
// of the bare names matched, so the panel concluded it was a TCP transport,
// dialled the kharej tunnel port — where pck has no socket at all, by design —
// and read the refusal as the tunnel being down. Only the dialling side runs
// that probe, which is why kharej stayed green and Iran did not.
func TestLayer3TransportsAreDatagram(t *testing.T) {
	for _, transport := range []string{"l3/udp", "l3/pck", "l3/xdi", "l3/spoof"} {
		if !IsDatagram(transport) {
			t.Errorf("%s is not treated as a datagram transport, so the panel will TCP-probe it", transport)
		}
	}
}

// The direct layer-4 transports are real TCP, so a TCP probe is right for them
// and must stay that way.
func TestDirectTransportsAreNotDatagram(t *testing.T) {
	for _, transport := range []string{"direct/tcp", "direct/stealth", "direct/ws", "direct/wss"} {
		if IsDatagram(transport) {
			t.Errorf("%s was treated as a datagram transport, so it loses its TCP liveness probe", transport)
		}
	}
}

// And the reverse transports must be classified exactly as they always were.
func TestReverseTransportClassificationIsUnchanged(t *testing.T) {
	for _, transport := range []string{"udp", "kcp", "xdi", "quic", "spoof", "pck"} {
		if !IsDatagram(transport) {
			t.Errorf("reverse transport %q stopped being a datagram transport", transport)
		}
	}
	for _, transport := range []string{"tcp", "tcpmux", "ws", "wss", "wsmux", "wssmux", "stealth"} {
		if IsDatagram(transport) {
			t.Errorf("reverse transport %q became a datagram transport", transport)
		}
	}
}
