package manage

import (
	"net"
	"strings"
	"testing"
)

// Adding a transport means touching a dozen places that each fail quietly when
// missed: a predicate that leaves it out of the KCP family gives it a zero-sized
// window, one that leaves it out of the datagram family has Diagnose probe a TCP
// socket that does not exist, and a menu that does not list it makes the whole
// thing unreachable. None of those is a compile error and none is visible until
// somebody builds a tunnel and it does not work.

// pck is a KCP transport underneath, so the presets must fill its window and
// tick interval. Without this it would run with a zero window and carry nothing.
func TestPckIsTunedByThePresets(t *testing.T) {
	for _, p := range []string{PresetBalance, PresetTurbo, PresetAggressive} {
		s := TunnelSpec{Role: "server", Transport: "pck"}
		ApplyPreset(&s, p)
		if s.KCPSndWnd <= 0 || s.KCPRcvWnd <= 0 || s.KCPInterval <= 0 || s.KCPMTU <= 0 {
			t.Fatalf("%s left pck untuned: mtu %d interval %d wnd %d/%d",
				p, s.KCPMTU, s.KCPInterval, s.KCPSndWnd, s.KCPRcvWnd)
		}
	}
}

// The predicates decide what the rest of the program believes about it. Each of
// these has a consequence spelled out in the message, because "the predicate is
// wrong" is not something anyone would chase from the symptom.
func TestPckPredicates(t *testing.T) {
	if !isKCP("pck") {
		t.Fatal("pck is not in the KCP family — its kcp_* settings would never be written to the config")
	}
	if !isMux("pck") {
		t.Fatal("pck is not in the mux family — its smux settings would be left at zero")
	}
	if !isDatagram("pck") {
		t.Fatal("pck is not in the datagram family — Diagnose would probe a TCP socket that does not exist, and Edit would offer an MSS clamp with nothing to clamp")
	}
	if !supportsProxyProtocol("pck") {
		t.Fatal("pck cannot carry the real client IP, but it multiplexes and has somewhere to put the header")
	}
	if !validTransport("pck") {
		t.Fatal("pck is not a valid transport — every edit and creation path would refuse it")
	}
}

// It has to be reachable from the menu, in the family the operator would look
// in for it.
func TestPckIsOfferedInTheTCPFamily(t *testing.T) {
	for _, g := range transportGroups {
		if g.label != "TCP" {
			continue
		}
		for _, e := range g.entries {
			if e.value == "pck" {
				if e.label == "" || e.desc == "" {
					t.Fatal("the pck entry has no label or description")
				}
				return
			}
		}
		t.Fatal("pck is not listed under the TCP family, so it cannot be chosen in setup")
	}
	t.Fatal("there is no TCP family in the transport menu")
}

// transportLabel drives the panel and every log line. An unlisted transport
// falls back to its upper-cased value, which would read as a bug.
func TestPckHasADisplayName(t *testing.T) {
	if got := transportLabel("pck"); got != "TCP + PCK" {
		t.Fatalf("pck displays as %q", got)
	}
}

// The carrier's settings must survive a save. They are all optional, so the
// failure mode of not writing them is a tunnel that silently reverts to
// automatic detection on the next edit.
func TestPckSettingsAreWrittenAndOnlyWhenSet(t *testing.T) {
	s := TunnelSpec{
		Role: "server", Name: "t", Transport: "pck",
		BindAddr: "0.0.0.0:9999", Token: "tok", Ports: []string{"443"},
		PckInterface: "eth0", PckGatewayMAC: "aa:bb:cc:dd:ee:ff",
		PckFlags: []string{"PA", "A"},
	}
	ApplyPreset(&s, PresetTurbo)
	out := s.Render()
	for _, want := range []string{
		`pck_interface = "eth0"`,
		`pck_gateway_mac = "aa:bb:cc:dd:ee:ff"`,
		`pck_flags = ["PA", "A"]`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the config does not contain %s:\n%s", want, out)
		}
	}

	// A tunnel that answered none of the optional questions must not grow empty
	// keys, or the config stops saying what was chosen and what was defaulted.
	bare := TunnelSpec{
		Role: "server", Name: "t", Transport: "pck",
		BindAddr: "0.0.0.0:9999", Token: "tok", Ports: []string{"443"},
	}
	ApplyPreset(&bare, PresetTurbo)
	if got := bare.Render(); strings.Contains(got, "pck_") {
		t.Fatalf("an unconfigured pck tunnel wrote pck_ keys anyway:\n%s", got)
	}

	// And no other transport carries them.
	other := s
	other.Transport = "tcp"
	if got := other.Render(); strings.Contains(got, "pck_") {
		t.Fatalf("a tcp tunnel carried the pck settings:\n%s", got)
	}
}

// pck binds no socket, but it still needs the TCP port to itself: a real
// listener there would receive the tunnel's segments and answer them. Every
// other datagram transport is checked against UDP instead, so this is the one
// place the family it belongs to gives the wrong answer.
func TestPckPortIsCheckedAsTCP(t *testing.T) {
	// The wildcard, because that is what PortInUse itself binds to test with —
	// a loopback-only listener does not conflict with it everywhere.
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Skipf("cannot open a listener here: %v", err)
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("unexpected listener address %q", ln.Addr())
	}

	if !TunnelPortInUse("pck", port) {
		t.Fatalf("port %s has a TCP listener on it but pck reports it free", port)
	}
	// The contrast that makes the point: the same port is free as far as a
	// genuine datagram transport is concerned.
	if TunnelPortInUse("kcp", port) {
		t.Fatalf("port %s/udp was reported busy by a TCP listener", port)
	}
}
