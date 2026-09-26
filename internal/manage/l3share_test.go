package manage

import (
	"reflect"
	"testing"

	"github.com/backpack/backpack/config"
)

func iranL3(name, peer string, ports ...string) l3Tunnel {
	return l3Tunnel{T: Tunnel{Name: name}, L: config.L3Config{Mode: "dial", PeerIP: peer, Ports: ports}}
}

// The second kharej asking for the first one's port gets it shared, with the
// first tunnel's mapping growing a second backend.
func TestASecondKharejSharesTheFirstOnesPort(t *testing.T) {
	first := iranL3("kharej-a", "10.10.0.2", "443", "8080")
	keep, shares, clash := planL3Sharing([]string{"443", "2053"}, "10.10.1.2", []l3Tunnel{first})

	if !reflect.DeepEqual(keep, []string{"2053"}) {
		t.Fatalf("keep = %v, want only the port nobody holds", keep)
	}
	if len(clash) != 0 {
		t.Fatalf("clash = %v", clash)
	}
	if len(shares) != 1 || shares[0].OldSpec != "443" ||
		shares[0].NewSpec != "443=10.10.0.2:443|10.10.1.2:443" {
		t.Fatalf("shares = %+v", shares)
	}
}

// A third kharej extends the mapping the second one already made.
func TestAThirdKharejJoinsAnAlreadySharedPort(t *testing.T) {
	first := iranL3("kharej-a", "10.10.0.2", "443=10.10.0.2:443|10.10.1.2:443")
	_, shares, _ := planL3Sharing([]string{"443"}, "10.10.2.2/30", []l3Tunnel{first})
	if len(shares) != 1 || shares[0].NewSpec != "443=10.10.0.2:443|10.10.1.2:443|10.10.2.2:443" {
		t.Fatalf("shares = %+v", shares)
	}
}

// A different target port on the new kharej is kept as asked.
func TestASharedPortKeepsEachKharejsOwnTarget(t *testing.T) {
	first := iranL3("kharej-a", "10.10.0.2", "443=8443")
	_, shares, _ := planL3Sharing([]string{"443=9443"}, "10.10.1.2", []l3Tunnel{first})
	if len(shares) != 1 || shares[0].NewSpec != "443=10.10.0.2:8443|10.10.1.2:9443" {
		t.Fatalf("shares = %+v", shares)
	}
}

// Ranges cannot have several backends, so an overlap involving one is
// reported rather than guessed at.
func TestARangeOverlapIsAClash(t *testing.T) {
	first := iranL3("kharej-a", "10.10.0.2", "10000-10009")
	keep, shares, clash := planL3Sharing([]string{"10005", "443"}, "10.10.1.2", []l3Tunnel{first})
	if len(shares) != 0 || !reflect.DeepEqual(clash, []string{"10005"}) || !reflect.DeepEqual(keep, []string{"443"}) {
		t.Fatalf("keep %v shares %+v clash %v", keep, shares, clash)
	}
}

// Nothing overlapping, nothing to do.
func TestNoOverlapKeepsEverything(t *testing.T) {
	keep, shares, clash := planL3Sharing([]string{"443"}, "10.10.1.2", []l3Tunnel{iranL3("a", "10.10.0.2", "80")})
	if !reflect.DeepEqual(keep, []string{"443"}) || len(shares) != 0 || len(clash) != 0 {
		t.Fatalf("keep %v shares %+v clash %v", keep, shares, clash)
	}
}

// The kharej is told its address with the prefix it needs.
func TestTheKharejIsToldItsAddressWithThePrefix(t *testing.T) {
	cfg := l3Spec{LocalIP: "10.10.1.1/30", PeerIP: "10.10.1.2"}
	if got := l3PeerWithPrefix(cfg); got != "10.10.1.2/30" {
		t.Fatalf("got %q", got)
	}
}

// Two new ports landing on the same existing tunnel rewrite it once, with
// both changes.
func TestSharesAreGroupedPerTunnel(t *testing.T) {
	first := iranL3("kharej-a", "10.10.0.2", "443", "80", "22")
	_, shares, _ := planL3Sharing([]string{"443", "80"}, "10.10.1.2", []l3Tunnel{first})
	groups := groupShares(shares)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	want := []string{"443=10.10.0.2:443|10.10.1.2:443", "80=10.10.0.2:80|10.10.1.2:80", "22"}
	if !reflect.DeepEqual(groups[0].ports, want) {
		t.Fatalf("ports = %v, want %v", groups[0].ports, want)
	}
	// And the tunnel it came from is not changed underneath.
	if !reflect.DeepEqual(first.L.Ports, []string{"443", "80", "22"}) {
		t.Fatalf("the original port list was modified: %v", first.L.Ports)
	}
}

// Two kharej tunnels listening on one UDP port cannot both run: the second
// fails to bind and restarts for ever. Two Iran servers set up with the
// wizard's default port 9000 land exactly there.
func TestAKharejRefusesATunnelPortAnotherTunnelListensOn(t *testing.T) {
	existing := []l3Tunnel{{
		T: Tunnel{Name: "iran-a"},
		L: config.L3Config{Mode: "listen", Carrier: "udp", Addr: "0.0.0.0:9000", Paths: 4},
	}}
	for _, tc := range []struct {
		carrier string
		port    int
		paths   int
		clash   bool
	}{
		{"udp", 9000, 0, true},
		{"quic", 9003, 0, true}, // inside iran-a's 9000-9003
		{"udp", 8998, 3, true},  // 8998-9000 reaches it
		{"udp", 9004, 0, false},
		{"xdi", 9000, 0, false}, // no UDP socket of its own
	} {
		got := l3ListenClash("iran-b", tc.carrier, tc.port, tc.paths, existing)
		if (got != "") != tc.clash {
			t.Errorf("%s on %d (paths %d): clash %q, want clash=%v", tc.carrier, tc.port, tc.paths, got, tc.clash)
		}
	}
}
