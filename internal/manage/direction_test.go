package manage

import (
	"strings"
	"testing"
)

// The direct kinds must dial OUT from Iran. That is the whole point of the
// word: a reverse tunnel has kharej dialling in to Iran, and where that
// inbound connection cannot be made the tunnel never comes up. Direct turns it
// around. If Iran ever ended up listening for the tunnel, the feature would be
// a reverse tunnel with different key names.
func TestDirectMeansIranDialsOut(t *testing.T) {
	l3iran := l3Spec{Side: sideIran, Carrier: "xdi", Encap: "gre",
		Addr: "203.0.113.9:9000", Token: "t", Iface: "bp0",
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1371}.Render()
	l3kharej := l3Spec{Side: sideKharej, Carrier: "xdi", Encap: "gre",
		Addr: "0.0.0.0:9000", Token: "t", Iface: "bp0",
		LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1371}.Render()

	// Iran is given the kharej server's real address to reach out to, and is
	// never handed a bind address for the tunnel.
	if !strings.Contains(l3iran, `addr         = "203.0.113.9:9000"`) || strings.Contains(l3iran, "0.0.0.0") {
		t.Fatalf("the Iran side was not given the kharej address to dial:\n%s", l3iran)
	}
	if !strings.Contains(l3iran, `mode         = "dial"`) {
		t.Fatalf("the Iran side of a layer-3 tunnel does not dial:\n%s", l3iran)
	}
	if !strings.Contains(l3kharej, `mode         = "listen"`) {
		t.Fatalf("the kharej side of a layer-3 tunnel does not listen:\n%s", l3kharej)
	}

	// The display predicates have to agree, or the screens describe a tunnel
	// that is not the one running.
	if !DialsOut(Tunnel{Role: "iran"}) {
		t.Error("the Iran side is not reported as the side that dials out")
	}
	if DialsOut(Tunnel{Role: "kharej"}) {
		t.Error("the kharej side is reported as dialling out")
	}
	// And it is the exact mirror of a reverse tunnel, where Iran is the server
	// that waits and kharej is the client that dials in.
	if DialsOut(Tunnel{Role: "server"}) {
		t.Error("a reverse server was reported as dialling out")
	}
	if !DialsOut(Tunnel{Role: "client"}) {
		t.Error("a reverse client was not reported as dialling out")
	}
}
